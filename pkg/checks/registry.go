package checks

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// Scope names one of the four dispatch phases. Three of them are not
// interchangeable: the name checks run BOTH over collected files and over
// archive file lists, archive content is a third phase and the repository a
// fourth.
type Scope uint8

const (
	ScopeFile            Scope = iota // a collected file
	ScopeArchiveFileList              // one entry of an archive's member-name list
	ScopeArchiveMember                // the content of an archive's members
	ScopeRepository                   // the whole collected file set
	NumScopes
)

var scopeNames = [NumScopes]string{"file", "archive-file-list", "archive-member", "repository"}

func (s Scope) String() string {
	if s < NumScopes {
		return scopeNames[s]
	}
	return fmt.Sprintf("scope(%d)", uint8(s))
}

// ParseScope maps a declared scope name onto its Scope.
func ParseScope(name string) (Scope, error) {
	for i, known := range scopeNames {
		if known == name {
			return Scope(i), nil
		}
	}
	return 0, fmt.Errorf("unknown scope %q (want one of %s)", name, strings.Join(scopeNames[:], ", "))
}

// ScopeSet is the set of scopes a check may serve. A rule may name a subset,
// defaulting to the CheckDef's.
type ScopeSet uint8

// ScopesOf builds the set of the given scopes. It is exported so a CheckDef
// literal built outside this package (the dispatch tests) can declare its
// scopes.
func ScopesOf(list ...Scope) ScopeSet {
	var set ScopeSet
	for _, s := range list {
		set |= 1 << s
	}
	return set
}

// Has reports whether the set contains scope.
func (s ScopeSet) Has(scope Scope) bool { return s&(1<<scope) != 0 }

// CheckDef is one registered check: its identity, the scopes it may serve, the
// loader that turns a rule's declared parameters into a bound runner, and the
// acquisition it performs. Exactly one of RunFile / RunRepository is set,
// matching Scopes, so the dispatcher never inspects which.
type CheckDef struct {
	// Name is rendered verbatim by every renderer; renaming it changes user-facing output.
	Name   string
	Scopes ScopeSet

	// Bind decodes and type-checks a rule's parameters ONCE, at load, and
	// returns a rule bound to them - so nothing is type-asserted afterwards.
	// Called on the ZERO RuleSpec it yields the check's defaults: the defaults
	// live in Bind itself, which is what default-rule synthesis relies on. Bind
	// does not fill the dispatch selector in: a selector carries no check
	// knowledge, so utils.Compile compiles one per scope and sets it.
	Bind func(spec config.RuleSpec, general *config.GeneralConfig) (*BoundRule, error)

	// RunFile acquires this file's content once and hands it to every rule that
	// matched it, so the invocation count per (file, check) is independent of
	// the rule count. ctx bounds the SCAN of acquired content, and only as an
	// UPPER BOUND: an implementation may ignore ctx entirely (the name checks
	// do), and none observes it more finely than once per archive member, per
	// body entry or per streamed chunk - never inside a byte-scan loop. A fired
	// ctx returns the messages collected so far. Acquisition itself does not
	// observe ctx: a single large PDF or OOXML container is extracted whole
	// before the first cancellation point. scope names the dispatch phase; only
	// the keyword check reads it, to tell a file's own content from an archive's
	// members. batch carries what the whole invocation shares - see Batch.
	RunFile func(ctx context.Context, file structs.File, scope Scope, batch *Batch, rules []*BoundRule) []structs.Message

	// RunRepository is RunFile's twin for the checks that need the whole file
	// set rather than one file.
	RunRepository func(ctx context.Context, repository structs.Repository, batch *Batch, rules []*BoundRule) []structs.Message
}

// Batch is the state one (check, scope) plan entry shares across ALL its rules.
// It lives on the plan entry rather than on every BoundRule because what it
// holds is a property of the acquisition, which happens once per (file, check),
// not once per rule.
type Batch struct {
	// limits and maxContentScan are the effective scan bounds, resolved once at
	// load. The acquisition reads them here rather than from [general] at scan
	// time, so per-rule overrides later are a config-surface change only.
	limits         readers.ArchiveLimits
	maxContentScan int64

	// Admit is the member admission filter an archive-member acquisition runs in
	// front of the per-rule gates: the single member rule's own member selector -
	// every shipped config's case - and the union of their literals otherwise.
	// utils.Compile builds it; nil admits every member.
	Admit *selector.Selector

	// PerRule says the per-rule member gates must still be consulted after
	// Admit, because Admit is not one rule's own filter. utils.Compile decides
	// it over the WHOLE plan; it must never be inferred from the rules that
	// happen to match one archive, which says nothing about what Admit is.
	PerRule bool
}

// NewBatch resolves the scan bounds every rule of one plan entry shares.
func NewBatch(general *config.GeneralConfig) *Batch {
	return &Batch{limits: archiveLimits(general), maxContentScan: general.MaxContentScanFileSize}
}

// reporting says how a rule reports the findings of one body entry: joined into
// one message, joined and cited by entry index (OOXML and other binary bodies)
// or by page (PDF), or one message per keyword (a streamed file, whose chunks
// the caller deduplicates).
type reporting uint8

const (
	reportJoined reporting = iota
	reportIndexed
	reportPaged
	reportEach
)

// BoundRule is one rule of one check, produced once at load. Its runner is a
// closure over this rule's concrete typed parameters - its keywords, its
// matcher, its info string, its disallowed-name set - never a sibling field
// over an interface value. Exactly one of Apply / ApplyRepo is set, matching
// the CheckDef.
type BoundRule struct {
	Rule string            // rule name: diagnostics, and Message.Rule
	sel  selector.Selector // the DISPATCH gate, compiled per scope by utils.Compile

	// Member is the archive-member gate, which reads the same configured lists
	// under the other legacy semantics: case-insensitive literals over the
	// member path. nil admits every member. Only an archive-member rule
	// carries one.
	Member *selector.Selector

	// unfiltered caches "the dispatch selector admits everything", the shipped
	// configs' case: the selection pass tests one bool per (file, rule) instead
	// of walking an empty selector. Kept true by SetSelectors, the only writer
	// of the dispatch selector.
	unfiltered bool

	// baseNames says the dispatch gate's "name" subject means the BASE name of
	// the file's member path (archive-file-list scope of a [[rule]], where the
	// synthetic file's Name is the full member path). Legacy rules never set
	// it: their gates historically read the whole member path.
	baseNames bool

	// apply scans one acquisition: its entries, their shared lowercase copies -
	// lowered once per acquisition, never once per rule - and how a finding is
	// reported. Both slices are nil for checks that read no content. ctx is
	// observed per body entry at most; the name checks ignore it.
	//
	// CONTRACT: body and lowered are BORROWED for the duration of the call. An
	// implementation may read them but must never retain them (the archive
	// acquisition reuses one pair of slices for every member, and the streamed
	// one hands out the chunk buffer itself).
	apply     func(ctx context.Context, file structs.File, body, lowered [][]byte, report reporting) []structs.Message
	applyRepo func(ctx context.Context, repository structs.Repository, batch *Batch, sel *selector.Selector) []structs.Message
}

// SetSelectors gives a rule the selectors CompileRuleSelectors compiled for
// ONE scope: the gate decides whether the rule is dispatched for a file (or an
// archive), the member selector whether it sees an individual archive member,
// and BaseNames whether the gate's "name" subject means the base of a member
// path. It is the only writer of the dispatch selector, because it also
// refreshes the cached unfiltered answer.
func (r *BoundRule) SetSelectors(s RuleSelectors) {
	r.sel = s.Gate
	r.Member = s.Member
	r.baseNames = s.BaseNames
	r.unfiltered = s.Gate.Unfiltered()
}

// Match reports whether this rule's dispatch gate admits the file, against the
// subject the selector declares. It is the selection pass's inner loop, run once
// per (file, rule), so the "admits everything" case - every shipped config's -
// is a cached bool and the body stays small enough to inline; the real matching
// lives in matchSubject, which the empty case never calls.
// Unfiltered reports whether the dispatch selector admits every file - the
// shipped configs' case. The dispatcher's fast path reads it once per pass:
// when every rule of a scope is unfiltered, selection is the identity and the
// per-file work items can share the plan's own entries.
func (r *BoundRule) Unfiltered() bool { return r.unfiltered }

func (r *BoundRule) Match(file structs.File) bool {
	if r.unfiltered {
		return true
	}
	return r.matchSubject(file)
}

func (r *BoundRule) matchSubject(file structs.File) bool {
	if r.sel.Subject() == selector.SubjectPath {
		return r.sel.Match(file.RelPath)
	}
	name := file.Name
	if r.baseNames {
		name = path.Base(name)
	}
	return r.sel.Match(name)
}

// matchMember reports whether this rule's member gate admits the member path.
// It takes the path rather than a structs.File so the member loop can decide
// without building one.
func (r *BoundRule) matchMember(memberPath string) bool {
	if r.Member == nil {
		return true
	}
	if r.Member.Subject() == selector.SubjectName {
		return r.Member.Match(path.Base(memberPath))
	}
	return r.Member.Match(memberPath)
}

// narrow hands a repository rule only the files its selector admits. An empty
// selector skips the copy and passes the caller's slice unchanged - every
// shipped config's case. Narrowing does not gate the rule: one whose selector
// admits nothing still runs, and can report over an empty set.
func (r *BoundRule) narrow(repository structs.Repository) structs.Repository {
	if r.sel.Unfiltered() {
		return repository
	}
	files := make([]structs.File, 0, len(repository.Files))
	for _, file := range repository.Files {
		if r.Match(file) {
			files = append(files, file)
		}
	}
	repository.Files = files
	return repository
}

// tag stamps a rule's name onto the findings it produced. Messages a check
// emits for ITSELF - the skip acknowledgements - keep an empty Rule: they are
// the acquisition's own voice, not the rule's, and no rule name explains them.
func tag(rule string, messages []structs.Message) []structs.Message {
	for i := range messages {
		if messages[i].Skipped {
			continue
		}
		messages[i].Rule = rule
	}
	return messages
}

// Registry holds every registered check. Its DECLARED ORDER is load-bearing:
// utils.Compile adds the rules to the plan in it, so it is the order the
// dispatch runs the checks of one file in, and therefore the order findings are
// rendered in. Lookup is by name; Defs preserves the order.
type Registry struct {
	defs  []CheckDef
	index map[string]int
}

// Lookup returns one check's definition.
func (r Registry) Lookup(name string) (CheckDef, bool) {
	i, known := r.index[name]
	if !known {
		return CheckDef{}, false
	}
	return r.defs[i], true
}

// Defs returns the definitions in declared order, as a copy: the order is
// load-bearing, so a caller must not be able to reorder the registry's own
// slice through it.
func (r Registry) Defs() []CheckDef {
	return append([]CheckDef(nil), r.defs...)
}

// Len returns the number of registered checks.
func (r Registry) Len() int { return len(r.defs) }

// NewRegistry returns the check registry: a constructed value, never an
// init()-populated global, so a caller always knows where it came from.
//
// The order below is the dispatch order the five hard-coded tables this
// registry replaced had (BY_FILE, BY_FILE_ON_ARCHIVE_FILE_LIST,
// BY_FILE_ON_ARCHIVE, BY_REPOSITORY_SECRETS, BY_REPOSITORY - the last two ran as
// separate phases, secrets first). Rendered message order follows it, so it is
// pinned by test, not incidental: do not sort it.
func NewRegistry() Registry {
	defs := []CheckDef{
		{Name: "HasOnlyASCII", Scopes: ScopesOf(ScopeFile, ScopeArchiveFileList), Bind: bindNoParams(hasOnlyASCII), RunFile: runNameRules},
		{Name: "HasNoWhiteSpace", Scopes: ScopesOf(ScopeFile, ScopeArchiveFileList), Bind: bindNoParams(hasNoWhiteSpace), RunFile: runNameRules},
		{Name: "IsFreeOfKeywords", Scopes: ScopesOf(ScopeFile, ScopeArchiveMember), Bind: bindKeywords, RunFile: runKeywords},
		{Name: "IsValidName", Scopes: ScopesOf(ScopeFile, ScopeArchiveFileList), Bind: bindValidName, RunFile: runNameRules},
		{Name: "HasFileNameSpecialChars", Scopes: ScopesOf(ScopeFile), Bind: bindNoParams(hasFileNameSpecialChars), RunFile: runNameRules},
		{Name: "IsFileNameTooLong", Scopes: ScopesOf(ScopeFile), Bind: bindNoParams(isFileNameTooLong), RunFile: runNameRules},
		{Name: "IsFreeOfSecrets", Scopes: ScopesOf(ScopeRepository), Bind: bindSecrets, RunRepository: runRepositoryRules},
		{Name: "HasReadme", Scopes: ScopesOf(ScopeRepository), Bind: bindHasReadme, RunRepository: runRepositoryRules},
		{Name: "ReadMeContainsTOC", Scopes: ScopesOf(ScopeRepository), Bind: bindReadMeContainsTOC, RunRepository: runRepositoryRules},
	}
	index := make(map[string]int, len(defs))
	for i, def := range defs {
		index[def.Name] = i
	}
	return Registry{defs: defs, index: index}
}

// runNameRules is RunFile for the checks that read no content: every rule that
// matched the file reports through its own bound parameters. ctx is forwarded
// but never consulted: a name check costs less than testing it would.
func runNameRules(ctx context.Context, file structs.File, _ Scope, _ *Batch, rules []*BoundRule) []structs.Message {
	var messages []structs.Message
	for _, rule := range rules {
		messages = append(messages, tag(rule.Rule, rule.apply(ctx, file, nil, nil, reportJoined))...)
	}
	return messages
}

// runRepositoryRules is RunRepository for every repository check: each rule
// sees the repository narrowed by its own selector, and is handed that same
// selector plus the batch's scan bounds - it never reads them back off itself.
func runRepositoryRules(ctx context.Context, repository structs.Repository, batch *Batch, rules []*BoundRule) []structs.Message {
	var messages []structs.Message
	for _, rule := range rules {
		messages = append(messages, tag(rule.Rule, rule.applyRepo(ctx, rule.narrow(repository), batch, &rule.sel))...)
	}
	return messages
}

// bindNoParams binds a check that takes no parameters at all: the rule carries
// nothing but its selector, and the zero RuleSpec is as good as any other.
func bindNoParams(check func(structs.File) []structs.Message) func(config.RuleSpec, *config.GeneralConfig) (*BoundRule, error) {
	return func(spec config.RuleSpec, _ *config.GeneralConfig) (*BoundRule, error) {
		if err := rejectParams(spec); err != nil {
			return nil, err
		}
		return &BoundRule{
			Rule: spec.Name,
			apply: func(_ context.Context, file structs.File, _, _ [][]byte, _ reporting) []structs.Message {
				return check(file)
			},
		}, nil
	}
}

// ruleSets returns a rule's parameter sets, the unit the batched runners loop
// over: each [rule.params] table is one set, a translated legacy section's
// keywordArguments list its N. The zero RuleSpec carries none, which is the
// check's default. allowed names the keys the check reads; on the [[rule]]
// surface any other key is a load error - legacy sets keep their lenient
// reading until the sugar is removed.
func ruleSets(spec config.RuleSpec, allowed ...string) ([]map[string]interface{}, error) {
	if spec.Legacy {
		return spec.Params, nil
	}
	for _, set := range spec.Params {
		keys := make([]string, 0, len(set))
		for key := range set {
			keys = append(keys, key)
		}
		sort.Strings(keys) // map order is random; the reported key must not be
		for _, key := range keys {
			if !slices.Contains(allowed, key) {
				if len(allowed) == 0 {
					return nil, fmt.Errorf("check %q takes no parameters", spec.Check)
				}
				return nil, fmt.Errorf("params: unknown key %q (check %q takes %s)", key, spec.Check, strings.Join(allowed, ", "))
			}
		}
	}
	return spec.Params, nil
}

// rejectParams refuses parameters on a check that takes none, so a typo in a
// section name is a load error rather than a silently inert setting.
func rejectParams(spec config.RuleSpec) error {
	sets, err := ruleSets(spec)
	if err != nil {
		return err
	}
	if len(sets) > 0 {
		return fmt.Errorf("check %q takes no parameters", spec.Check)
	}
	return nil
}

// stringList type-checks one list parameter of one set, once, at load.
func stringList(set map[string]interface{}, key string, index int) ([]string, error) {
	value, present := set[key]
	if !present {
		return nil, fmt.Errorf("parameter set %d: %q is required", index+1, key)
	}
	list, ok := value.([]string)
	if !ok {
		return nil, fmt.Errorf("parameter set %d: %q must be a list of strings, got %T", index+1, key, value)
	}
	return list, nil
}

// stringParam type-checks one string parameter of one set, once, at load.
func stringParam(set map[string]interface{}, key string, index int) (string, error) {
	value, present := set[key]
	if !present {
		return "", fmt.Errorf("parameter set %d: %q is required", index+1, key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("parameter set %d: %q must be a string, got %T", index+1, key, value)
	}
	return text, nil
}
