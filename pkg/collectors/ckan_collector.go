package collectors

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// maxCKANResponseBytes caps how much of a CKAN package_show response is read.
// Package metadata for even very large packages is a few MB of JSON; the cap
// only guards against a misbehaving/compromised upstream streaming unbounded
// data into memory. A response exceeding it is treated as a transport failure
// (mapped to ckan_unavailable), like any other unusable reply. It is a var
// (not a const) so tests can lower it without allocating the full default.
var maxCKANResponseBytes int64 = 50 << 20 // 50 MiB

// ErrResourceUnreadable is a sentinel carried (via errors.Is) when a
// url_type=="upload" resource's file cannot be located/read under the
// configured ckan_storage_path: either it escaped the storage root, or it is
// missing on disk. The server maps this to the "resource_unreadable" error
// code (spec §3); the CLI surfaces it as a plain collector error.
var ErrResourceUnreadable = errors.New("resource file is unreadable")

// MalformedResourceError carries a user-facing message describing a CKAN
// resource that cannot be processed: either it is missing BOTH url_type and url,
// or it is an upload missing required metadata (name/url/size). The message is
// authored for the end user and is safe to surface verbatim — it names only the
// resource and the package, never a token, URL or internal path. The server
// maps it to the "malformed_resource" error code (422) and shows Msg directly;
// the CLI surfaces it as a plain collector error.
type MalformedResourceError struct {
	Msg string
}

func (e *MalformedResourceError) Error() string { return e.Msg }

// CKANError carries the outcome of the single CKAN package_show call so the
// caller (the server) can map it to the fixed error catalogue (spec §3, §5)
// WITHOUT re-fetching. It distinguishes:
//   - a transport/connection failure (Transport == true, StatusCode == 0) →
//     mapped to ckan_unavailable;
//   - an HTTP status (StatusCode set), including a CKAN Action API body that
//     reports success:false even on HTTP 200 (StatusFromBody == true) →
//     mapped by status (404 → package_not_found, 401 → invalid_token,
//     403 → access_denied, 5xx → ckan_unavailable).
//
// The error message is deliberately non-secret: it never embeds the token or
// the package-id-carrying URL, only the status code.
type CKANError struct {
	StatusCode     int  // HTTP status; for a success:false body this is the effective status (e.g. 404)
	Transport      bool // true for a transport/connection error (no HTTP response)
	StatusFromBody bool // true when StatusCode was derived from a success:false body, not the HTTP status
}

func (e *CKANError) Error() string {
	if e.Transport {
		return "ckan request failed: transport error"
	}
	return fmt.Sprintf("ckan request failed with status %d", e.StatusCode)
}

// ckanActionErrorStatus inspects a parsed CKAN Action API body and, when it
// reports success:false, returns the effective HTTP status implied by
// error.__type plus that type string. CKAN may answer HTTP 200 with
// success:false (spec §3), so the body must be parsed, not just the status.
func ckanActionErrorStatus(body map[string]interface{}) (status int, errType string, isError bool) {
	success, ok := body["success"].(bool)
	if !ok || success {
		return 0, "", false
	}
	if errObj, ok := body["error"].(map[string]interface{}); ok {
		if t, ok := errObj["__type"].(string); ok {
			errType = t
		}
	}
	switch errType {
	case "Authorization Error":
		// CKAN uses this for both "not authorized" and "not found" under the
		// default reveal_private_datasets=false; treat it as 404 so it maps to
		// the combined package_not_found message (spec §3).
		status = http.StatusNotFound
	case "Not Found Error":
		status = http.StatusNotFound
	default:
		status = http.StatusInternalServerError
	}
	return status, errType, true
}

// Request performs the single CKAN package_show GET. The supplied context bounds
// the in-flight call: when its deadline fires (the server's hard requestTimeout,
// spec §2) or it is cancelled (client disconnect), client.Do and the subsequent
// body read are aborted instead of blocking on a slow/hung CKAN socket. A
// cancelled/expired context surfaces as a transport *CKANError (mapped to
// ckan_unavailable / 504 by the server).
func Request(ctx context.Context, url, ckanToken string, verifyTLS bool) (string, error) {

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !verifyTLS,
			// If verifyTLS=false => InsecureSkipVerify=true
		},
	}

	client := &http.Client{
		Transport: transport,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	if ckanToken != "" {
		req.Header.Set("Authorization", ckanToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		// Transport/connection failure (including a context deadline/cancel
		// aborting the in-flight call): no usable HTTP response. Never embed the
		// underlying error (it can carry the package-id-carrying URL) nor the
		// token; the server maps this to ckan_unavailable (and distinguishes a
		// deadline as 504 via the request context).
		output.GlobalLogger.Warning("CKAN request failed: transport error")
		return "", &CKANError{Transport: true}
	}
	defer resp.Body.Close()

	// Read one byte past the cap so an oversized response is detectable below.
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCKANResponseBytes+1))

	if resp.StatusCode != http.StatusOK {
		// Log a non-secret diagnostic: status code only. Never log the URL (it
		// carries the package id query) or the token (exfiltration vector).
		output.GlobalLogger.Warning("CKAN request failed with status code %d", resp.StatusCode)
		return "", &CKANError{StatusCode: resp.StatusCode}
	}

	if readErr != nil {
		// The connection died mid-body: same failure class as a failed dial — no
		// usable response. Never embed the underlying error (URL/token hygiene,
		// see above).
		output.GlobalLogger.Warning("CKAN request failed: transport error reading response body")
		return "", &CKANError{Transport: true}
	}
	if int64(len(bodyBytes)) > maxCKANResponseBytes {
		output.GlobalLogger.Warning("CKAN response exceeded the %d byte limit and was rejected", maxCKANResponseBytes)
		return "", &CKANError{Transport: true}
	}

	// HTTP 200 may still carry a CKAN Action API failure (success:false). Parse
	// the body and surface it as a structured CKANError so the server maps it.
	if parsed, perr := JSONToMap(string(bodyBytes)); perr == nil {
		if status, errType, isErr := ckanActionErrorStatus(parsed); isErr {
			output.GlobalLogger.Warning("CKAN request succeeded (200) but body reports failure (__type=%q)", errType)
			return "", &CKANError{StatusCode: status, StatusFromBody: true}
		}
	}

	return string(bodyBytes), nil
}

// JSON string to map
func JSONToMap(jsonStr string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &result)
	return result, err
}

// Check if the resource is a file
func resourceIsFile(resource map[string]interface{}) bool {
	if url_type, ok := resource["url_type"].(string); ok {
		return url_type == "upload"
	}
	return false
}

// isEmptyField reports whether a CKAN resource field is "empty": the key is
// absent, the value is JSON null, or it is an empty string.
func isEmptyField(resource map[string]interface{}, key string) bool {
	v, ok := resource[key]
	if !ok || v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

// resourceDisplayLabel returns a human-readable identifier for a resource,
// preferring its name, then its id, for use in error messages.
func resourceDisplayLabel(resource map[string]interface{}) string {
	if name, ok := resource["name"].(string); ok && name != "" {
		return name
	}
	if id, ok := resource["id"].(string); ok && id != "" {
		return id
	}
	return "<unknown>"
}

// packageLabel returns a human-readable identifier for the package, preferring
// its name (the slug the user requested), then its title.
func packageLabel(result map[string]interface{}) string {
	if name, ok := result["name"].(string); ok && name != "" {
		return name
	}
	if title, ok := result["title"].(string); ok && title != "" {
		return title
	}
	return "<unknown>"
}

// Expects parsed JSON and returns all resources of the CKAN package
func GetCKANResources(jsonMap map[string]interface{}) ([]structs.File, error) {
	files := []structs.File{}
	result, ok := jsonMap["result"].(map[string]interface{})
	if !ok {
		return files, nil
	}
	pkgName := packageLabel(result)

	resources, ok := result["resources"].([]interface{})
	if !ok {
		return files, nil
	}

	for _, resource := range resources {
		res, ok := resource.(map[string]interface{})
		if !ok {
			continue
		}

		// A resource missing BOTH url_type and url is malformed: stop collection
		// and tell the user exactly which resource so they can fix it.
		if isEmptyField(res, "url_type") && isEmptyField(res, "url") {
			return nil, &MalformedResourceError{Msg: fmt.Sprintf(
				"The resource '%s' in package '%s' is malformed and can not be processed. "+
					"The url_type is missing. Files should have a URL type 'upload', "+
					"external links should carry a URL, and DataStore resources use 'datastore'. "+
					"Something must have gone wrong creating this resource. Please recreate/reupload it.",
				resourceDisplayLabel(res), pkgName,
			)}
		}

		if resourceIsFile(res) {
			// An "upload" resource must carry name, url and size. Guard the type
			// assertions so malformed uploads return a clean error instead of panicking.
			name, okName := res["name"].(string)
			resURL, okURL := res["url"].(string)
			size, okSize := res["size"].(float64)
			if !okName || !okURL || !okSize {
				return nil, &MalformedResourceError{Msg: fmt.Sprintf(
					"The resource '%s' in package '%s' is an upload but is missing required "+
						"metadata (name, url or size) and can not be processed. "+
						"Something must have gone wrong creating this resource. Please recreate/reupload it.",
					resourceDisplayLabel(res), pkgName,
				)}
			}
			// Use ToFileWithDisplay to preserve CKAN resource name as DisplayName
			file := structs.ToFileWithDisplay(
				resURL, // path (will be converted to local path later)
				name,   // name
				name,   // displayName (CKAN resource name)
				int64(size),
				"",
				"", // archiveName (not in archive)
			)
			files = append(files, file)
		}
	}
	return files, nil
}

// getLocalResourcePath translates a CKAN resource download URL into the local
// FileStore path under ckanStoragePath, following CKAN's id-sharding layout
// (resources/<id[:3]>/<id[3:6]>/<id[6:]>).
//
// It returns "" with a nil error when the URL is malformed (caller treats this
// as "not resolvable, skip"), preserving the previous best-effort behaviour.
// It returns a non-nil error wrapping ErrResourceUnreadable when the resolved
// path escapes the configured storage root (path containment, spec §5) so the
// caller cannot read a file outside the mounted share.
func getLocalResourcePath(resourceURL string, ckanStoragePath string) (string, error) {

	parsedURL, err := neturl.Parse(resourceURL)
	if err != nil {
		return "", nil
	}
	parts := strings.Split(parsedURL.Path, "/")
	if len(parts) <= 4 {
		output.GlobalLogger.Warning("Error: resource URL has invalid format '%s' - are the resources restricted?", resourceURL)
		return "", nil // Return empty string instead of panicking
	}
	resourceID := parts[4]

	// Validate resourceID has at least 6 characters
	if len(resourceID) < 6 {
		output.GlobalLogger.Warning("Error: resource ID '%s' is too short (needs at least 6 characters)", resourceID)
		return "", nil // Return empty string instead of panicking
	}

	// Slice out parts: rsc_1, rsc_2, rsc_3
	rsc1 := resourceID[:3]
	rsc2 := resourceID[3:6]
	rsc3 := resourceID[6:]

	localResourcePath := fmt.Sprintf("%s/%s/%s", rsc1, rsc2, rsc3)

	// If ckanStoragePath ends with "/", remove the slash
	ckanStoragePath = strings.TrimSuffix(ckanStoragePath, "/")

	// If ckanStoragePath is not empty, ensure it ends with "resources/"
	if ckanStoragePath != "" {
		if !strings.HasSuffix(ckanStoragePath, "resources") {
			ckanStoragePath += "/resources"
		}
		ckanStoragePath += "/"
	}

	resolved := ckanStoragePath + localResourcePath

	// Path containment (spec §5): when a storage root is configured, clean the
	// resolved path and verify it stays under that root. The resource id is
	// attacker-influenceable (it comes from the CKAN response), so a crafted id
	// containing "../" or an absolute injection must never let us read outside
	// the mounted share.
	if ckanStoragePath != "" {
		clean, ok := containWithinRoot(ckanStoragePath, resolved)
		if !ok {
			//lint:ignore ST1005 user-facing message shown verbatim to the end user
			return "", fmt.Errorf(
				"Resource path '%s' escapes the configured storage root '%s' and was rejected: %w",
				filepath.Clean(resolved), filepath.Clean(ckanStoragePath), ErrResourceUnreadable,
			)
		}
		return clean, nil
	}

	return resolved, nil
}

// containWithinRoot cleans resolved and reports whether it stays inside root
// (root itself, or a path under root). It returns the cleaned path so callers
// use the normalized form. The root is resolved/cleaned once here so a resolved
// path that escapes via "../" or absolute injection is rejected.
func containWithinRoot(root, resolved string) (string, bool) {
	cleanRoot := filepath.Clean(root)
	clean := filepath.Clean(resolved)
	if clean == cleanRoot {
		return clean, true
	}
	if strings.HasPrefix(clean, cleanRoot+string(os.PathSeparator)) {
		return clean, true
	}
	return clean, false
}

// resolveLocalResource resolves an upload resource's local path and confirms the
// file exists on disk. A missing file (spec §5) yields an error wrapping
// ErrResourceUnreadable so the caller can map it to "resource_unreadable"
// instead of silently skipping or panicking.
func resolveLocalResource(resourceURL, displayLabel, ckanStoragePath string) (string, error) {
	path, err := getLocalResourcePath(resourceURL, ckanStoragePath)
	if err != nil {
		return "", err
	}
	// An unresolvable URL (empty path) is left as-is: there is no local file to
	// stat and the previous behaviour kept the original URL in File.Path.
	if path == "" {
		return resourceURL, nil
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			//lint:ignore ST1005 user-facing message shown verbatim to the end user
			return "", fmt.Errorf(
				"The file for resource '%s' is missing from storage and can not be read: %w",
				displayLabel, ErrResourceUnreadable,
			)
		}
		// Other stat errors (e.g. permission) are equally "unreadable".
		//lint:ignore ST1005 user-facing message shown verbatim to the end user
		return "", fmt.Errorf(
			"The file for resource '%s' could not be read from storage (%v): %w",
			displayLabel, statErr, ErrResourceUnreadable,
		)
	}
	return path, nil
}

// CkanCollector performs the single CKAN package_show call (spec §5) and
// resolves each upload resource to its local FileStore path. The context bounds
// the upstream HTTP call so a slow/hung CKAN cannot outlive the caller's
// deadline (spec §2). CLI callers with no deadline pass context.Background().
func CkanCollector(ctx context.Context, package_id string, config config.Config) ([]structs.File, error) {

	collectorName := "CkanCollector"

	// Guard the lookup once: a missing [collector.CkanCollector] section leaves a
	// nil *CollectorConfig in the map, so dereferencing .Attrs would panic. The
	// server validates this at boot (validateCkanCollector); this defence-in-depth
	// guard protects the CLI path too without changing server behaviour.
	cc, ok := config.Collectors[collectorName]
	if !ok || cc == nil {
		return nil, fmt.Errorf("CkanCollector configuration is missing: add a [collector.CkanCollector] section")
	}

	urlAttr, ok := cc.Attrs["url"].(string)
	if !ok {
		return nil, fmt.Errorf("url attribute not found or not a string")
	}

	// Always escape the package id when building the CKAN URL (spec §2): the id
	// is validated upstream, but escaping is a defence-in-depth invariant of URL
	// construction here.
	url := fmt.Sprintf("%s/api/3/action/package_show?id=%s", urlAttr, neturl.QueryEscape(package_id))

	token, ok := cc.Attrs["token"].(string)
	if !ok {
		return nil, fmt.Errorf("token attribute not found or not a string")
	}
	verify, ok := cc.Attrs["verify"].(bool)
	if !ok {
		return nil, fmt.Errorf("verify attribute not found or not a bool")
	}

	jsonStr, err := Request(ctx, url, token, verify)
	if err != nil {
		return nil, err
	}
	jsonMap, err := JSONToMap(jsonStr)
	if err != nil {
		return nil, err
	}

	files, err := GetCKANResources(jsonMap)
	if err != nil {
		return nil, err
	}

	localStoragePath, ok := cc.Attrs["ckan_storage_path"].(string)
	if !ok {
		return nil, fmt.Errorf("ckan_storage_path attribute not found or not a string")
	}
	// Iterate files and resolve each upload to its local FileStore path. Path
	// containment and the missing-on-disk check (spec §5) live in
	// resolveLocalResource; a failure there carries ErrResourceUnreadable.
	for i, file := range files {
		localPath, err := resolveLocalResource(file.Path, file.GetDisplayName(), localStoragePath)
		if err != nil {
			return nil, err
		}
		files[i].Path = localPath
	}

	return files, nil
}
