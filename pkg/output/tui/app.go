package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/eawag-rdm/pc/pkg/output"
)

// copyToClipboardOSC52 uses OSC 52 escape sequence to copy to clipboard.
// This works over SSH/tmux when the terminal supports it.
// Writes directly to /dev/tty to bypass tview's terminal capture.
func copyToClipboardOSC52(text string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()

	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// OSC 52 sequence: \033]52;c;<base64>\a
	_, err = fmt.Fprintf(tty, "\033]52;c;%s\a", encoded)
	return err
}

type App struct {
	app               *tview.Application
	data              *ScanResult
	subjectsList      *tview.List
	checksList        *tview.List
	leftSections      *tview.TextView // Header bar for Subjects/Checks switching
	leftContent       *tview.Flex     // Content area for subjects or checks list
	detailsContent    *tview.TextView // Content for selected section
	info              *tview.TextView
	controls          *tview.TextView
	progressBar       *tview.TextView // Progress bar for scanning
	flex              *tview.Flex
	leftPanel         *tview.Flex // Store reference to left panel for dynamic content
	rightPanel        *tview.Flex // Store reference to right panel for dynamic height
	currentView       string      // "subjects", "checks", or "details"
	currentSubject    string // Currently selected subject/check
	selectedSection   int    // Currently selected details section (0-3)
	selectedLeftPanel int    // Currently selected left panel (0=subjects, 1=checks)
	isScanning        bool   // Whether we're currently scanning
	startupCallback   func() // Called when TUI starts running
	location          string // Location/path being scanned (for summary)
	summaryModal      *tview.Flex     // Modal overlay for summary
	summaryTextView   *tview.TextView // Scrollable summary content
	summaryVisible    bool            // Track modal visibility
}

func NewApp(data *ScanResult) *App {
	app := &App{
		app:               tview.NewApplication(),
		data:              data,
		currentView:       "subjects",
		selectedSection:   0,
		selectedLeftPanel: 0, // Start with subjects selected
		isScanning:        false, // Not scanning for regular TUI
	}
	app.setupUI()
	return app
}

// NewScanningApp creates a new TUI app for live scanning with progress bar
func NewScanningApp() *App {
	// Create empty initial data
	emptyData := &ScanResult{
		Timestamp:             "Scanning...",
		Scanned:               []ScannedFile{},
		Skipped:               []SkippedFile{},
		DetailsSubjectFocused: []SubjectDetails{},
		DetailsCheckFocused:   []CheckDetails{},
		PDFFiles:              []string{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}
	
	app := &App{
		app:               tview.NewApplication(),
		data:              emptyData,
		currentView:       "subjects",
		selectedSection:   0,
		selectedLeftPanel: 0, // Start with subjects selected
		isScanning:        true, // Start in scanning mode
	}
	app.setupUI()
	
	// Set initial scanning message
	app.updateInfo()
	app.progressBar.SetText("Preparing to scan...")
	
	return app
}

func (a *App) setupUI() {
	// Create components
	a.subjectsList = tview.NewList().ShowSecondaryText(false)
	a.checksList = tview.NewList().ShowSecondaryText(false)
	a.leftSections = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	a.leftContent = tview.NewFlex().SetDirection(tview.FlexRow)
	a.detailsContent = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWrap(true)
	
	// Set up faster scrolling for details content
	a.detailsContent.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyUp:
			// Scroll up by 2 lines
			row, _ := a.detailsContent.GetScrollOffset()
			a.detailsContent.ScrollTo(row-2, 0)
			return nil
		case tcell.KeyDown:
			// Scroll down by 2 lines
			row, _ := a.detailsContent.GetScrollOffset()
			a.detailsContent.ScrollTo(row+2, 0)
			return nil
		}
		return event
	})
	a.info = tview.NewTextView().SetDynamicColors(true)
	a.controls = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	a.progressBar = tview.NewTextView().SetDynamicColors(true)

	// Set up borders and titles
	a.subjectsList.SetBorder(true).SetTitle(" Issues ")
	a.checksList.SetBorder(true).SetTitle(" Issues ")
	a.leftSections.SetBorder(true).SetTitle(" Focused on ")
	a.detailsContent.SetBorder(true).SetTitle(" Details ")
	a.info.SetBorder(true).SetTitle(" Summary ")
	a.controls.SetBorder(true).SetTitle(" Controls ")
	a.progressBar.SetBorder(true).SetTitle(" Scan Progress ")

	// Create left panel with all categories (subjects, checks, skipped, warnings, errors)
	a.leftPanel = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.leftSections, 6, 0, false).  // Increased height to accommodate all categories
		AddItem(a.leftContent, 0, 1, true)

	a.rightPanel = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.info, 6, 0, false).
		AddItem(a.detailsContent, 0, 1, false)

	mainContent := tview.NewFlex().
		AddItem(a.leftPanel, 0, 1, true).
		AddItem(a.rightPanel, 0, 1, false)  // Changed ratio to give more space to left panel

	// Main layout - always include progress bar (hidden when not scanning)
	a.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.controls, 3, 0, false).
		AddItem(mainContent, 0, 1, false).
		AddItem(a.progressBar, 3, 0, false)
	
	// Hide progress bar initially unless scanning
	if !a.isScanning {
		a.progressBar.SetText("")
	}

	// Populate data
	a.populateSubjectsList()
	a.populateChecksList()
	a.populateLeftSections()
	a.showSubjectsPanel() // Start with subjects visible
	a.updateInfo()
	a.updateControls()

	// Set up key bindings
	a.setupKeyBindings()

	// Set up resize handler for responsive sections
	a.setupResizeHandler()

	// Set up summary modal
	a.setupSummaryModal()

	// Set root
	a.app.SetRoot(a.flex, true)
}

func (a *App) populateSubjectsList() {
	a.subjectsList.Clear()
	
	// Store subject names for selection change handler
	var subjectNames []string
	
	// Add scanned files
	for _, file := range a.data.Scanned {
		issueCount := 0
		for _, issue := range file.Issues {
			issueCount += issue.IssueCount
		}
		
		mainText := fmt.Sprintf("%s (%d)", file.Filename, issueCount)
		
		a.subjectsList.AddItem(mainText, "", 0, nil)
		subjectNames = append(subjectNames, file.Filename)
	}

	// Add repository if it has issues
	for _, subject := range a.data.DetailsSubjectFocused {
		if subject.Subject == "repository" {
			issueCount := len(subject.Issues)
			
			mainText := fmt.Sprintf("repository (%d)", issueCount)
			
			a.subjectsList.AddItem(mainText, "", 0, nil)
			subjectNames = append(subjectNames, "repository")
			break
		}
	}
	
	// Set up selection change handler for automatic details update
	a.subjectsList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(subjectNames) {
			// Update current subject and refresh details
			a.currentSubject = subjectNames[index]
			if a.currentView == "subjects" {
				a.showSubjectDetails()
			}
		}
	})
}

func (a *App) populateChecksList() {
	a.checksList.Clear()
	
	// Store check names for selection change handler
	var checkNames []string
	
	for _, check := range a.data.DetailsCheckFocused {
		issueCount := len(check.Issues)
		
		mainText := fmt.Sprintf("%s (%d)", check.Checkname, issueCount)
		
		a.checksList.AddItem(mainText, "", 0, nil)
		checkNames = append(checkNames, check.Checkname)
	}
	
	// Set up selection change handler for automatic details update
	a.checksList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(checkNames) {
			// Update current check and refresh details
			a.currentSubject = checkNames[index]
			if a.currentView == "checks" {
				a.showCheckDetails()
			}
		}
	})
}

func (a *App) updateInfo() {
	totalScanned := len(a.data.Scanned)
	totalSkipped := 0
	if a.data.Skipped != nil {
		totalSkipped = len(a.data.Skipped)
	}
	totalIssues := 0
	
	// Count issues from scanned files
	for _, file := range a.data.Scanned {
		for _, issue := range file.Issues {
			totalIssues += issue.IssueCount
		}
	}
	
	// Add repository issues
	for _, subject := range a.data.DetailsSubjectFocused {
		if subject.Subject == "repository" {
			totalIssues += len(subject.Issues)
		}
	}

	info := fmt.Sprintf(
		"[yellow]PC Scanner Results[white]\n"+
		"Timestamp: %s\n"+
		"Scanned: %d  |  Skipped: %d\n"+
		"Issues: %d  |  Errors: %d  |  Warnings: %d",
		a.data.Timestamp,
		totalScanned,
		totalSkipped,
		totalIssues,
		len(a.data.Errors),
		len(a.data.Warnings),
	)
	
	a.info.SetText(info)
}

func (a *App) updateControls() {
	var controls string

	// Determine if TAB is available (only for Subjects/Checks that can switch to details)
	tabAvailable := a.currentView == "details" || a.currentView == "subjects" || a.currentView == "checks"

	if a.currentView == "details" {
		// When focused on details (right side), no left/right arrow navigation
		if tabAvailable {
			controls = "[yellow]TAB[white]=Issues  [yellow]↑↓[white]=Scroll  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]X[white]=Summary  [yellow]Q[white]=Quit"
		} else {
			controls = "[yellow]↑↓[white]=Scroll  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]X[white]=Summary  [yellow]Q[white]=Quit"
		}
	} else {
		// When focused on left side, show category navigation
		if tabAvailable {
			controls = "[yellow]TAB[white]=Details  [yellow]←→[white]=Categories  [yellow]↑↓[white]=Navigate  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]X[white]=Summary  [yellow]Q[white]=Quit"
		} else {
			controls = "[yellow]←→[white]=Categories  [yellow]↑↓[white]=Navigate  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]X[white]=Summary  [yellow]Q[white]=Quit"
		}
	}

	a.controls.SetText(controls)
}


func (a *App) setupResizeHandler() {
	// Set up a periodic refresh to check for size changes
	// This will handle terminal resize events
	// Width monitoring is no longer needed since detailsSections is removed
}

func (a *App) setupKeyBindings() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Handle summary modal input separately
		if a.summaryVisible {
			switch event.Key() {
			case tcell.KeyEsc:
				a.hideSummaryModal()
				return nil
			}
			switch event.Rune() {
			case 'x', 'X', 'q', 'Q':
				a.hideSummaryModal()
				return nil
			}
			// Allow scrolling in the modal
			return event
		}

		switch event.Key() {
		case tcell.KeyTab:
			a.switchFocus()
			return nil
		case tcell.KeyEsc, tcell.KeyCtrlC:
			a.app.Stop()
			return nil
		}

		switch event.Rune() {
		case 'q', 'Q':
			a.app.Stop()
			return nil
		case 's', 'S':
			a.focusSubjects()
			return nil
		case 'c', 'C':
			a.focusChecks()
			return nil
		case 'd', 'D':
			if a.currentSubject != "" {
				a.focusDetails()
			}
			return nil
		case 'x', 'X':
			a.showSummaryModal()
			return nil
		}

		// Handle arrow keys for navigation
		switch event.Key() {
		case tcell.KeyLeft:
			// Only navigate categories when focused on left side
			if a.currentView != "details" {
				a.navigateLeftPanelLeft()
			}
			return nil
		case tcell.KeyRight:
			// Only navigate categories when focused on left side
			if a.currentView != "details" {
				a.navigateLeftPanelRight()
			}
			return nil
		}

		return event
	})
}

func (a *App) switchFocus() {
	switch a.currentView {
	case "subjects", "checks":
		// Switch from left panel (navigation) to right panel (content)
		if a.currentSubject != "" {
			a.focusDetails()
		} else {
			// If no subject selected, stay in left panel but ensure proper focus
			if a.selectedLeftPanel == 0 {
				a.focusSubjects()
			} else {
				a.focusChecks()
			}
		}
	case "details":
		// Switch from right panel (content) back to left panel (navigation)
		if a.selectedLeftPanel == 0 {
			a.focusSubjects()
		} else {
			a.focusChecks()
		}
	}
}

func (a *App) focusSubjects() {
	a.currentView = "subjects"
	a.selectedLeftPanel = 0
	a.populateLeftSections()
	a.showSubjectsPanel()
	a.app.SetFocus(a.subjectsList)
	// Set colors: left navigation header = yellow, subjects list = green, others = white
	a.leftSections.SetBorderColor(tcell.ColorYellow)
	a.subjectsList.SetBorderColor(tcell.ColorGreen)
	a.checksList.SetBorderColor(tcell.ColorWhite)
	a.detailsContent.SetBorderColor(tcell.ColorWhite)
	a.updateControls()
}

func (a *App) focusChecks() {
	a.currentView = "checks"
	a.selectedLeftPanel = 1
	a.populateLeftSections()
	a.showChecksPanel()
	a.app.SetFocus(a.checksList)
	// Set colors: left navigation header = yellow, checks list = green, others = white
	a.leftSections.SetBorderColor(tcell.ColorYellow)
	a.subjectsList.SetBorderColor(tcell.ColorWhite)
	a.checksList.SetBorderColor(tcell.ColorGreen)
	a.detailsContent.SetBorderColor(tcell.ColorWhite)
	a.updateControls()
}

func (a *App) focusDetails() {
	a.currentView = "details"
	a.app.SetFocus(a.detailsContent)
	// Set colors: details sections header = yellow, details content = green, others = white
	a.leftSections.SetBorderColor(tcell.ColorWhite)
	a.subjectsList.SetBorderColor(tcell.ColorWhite)
	a.checksList.SetBorderColor(tcell.ColorWhite)
	a.detailsContent.SetBorderColor(tcell.ColorGreen)
	a.updateControls()
}

func (a *App) formatSectionsResponsive(sectionTexts []string) (string, int) {
	// Get the terminal width for the sections area
	// Use a reasonable default width since detailsSections is removed
	width := 80
	
	availableWidth := width - 4 // Account for borders and padding
	
	// Ensure minimum width
	if availableWidth < 20 {
		availableWidth = 60 // Fallback for initialization phase
	}
	
	// Remove color codes to calculate actual text length
	stripColors := func(text string) string {
		// Simple color stripping - remove [color] and [-:-] patterns
		result := text
		for {
			start := strings.Index(result, "[")
			if start == -1 {
				break
			}
			end := strings.Index(result[start:], "]")
			if end == -1 {
				break
			}
			result = result[:start] + result[start+end+1:]
		}
		return result
	}
	
	// Try to fit all sections on one line first
	singleLine := strings.Join(sectionTexts, "  ")
	if len(stripColors(singleLine)) <= availableWidth {
		return singleLine, 1
	}
	
	// If too wide, wrap to multiple lines
	lines := []string{}
	currentLine := ""
	
	for _, section := range sectionTexts {
		testLine := currentLine
		if testLine != "" {
			testLine += "  "
		}
		testLine += section
		
		if len(stripColors(testLine)) <= availableWidth {
			currentLine = testLine
		} else {
			// Start new line
			if currentLine != "" {
				lines = append(lines, currentLine)
			}
			currentLine = section
		}
	}
	
	// Add the last line
	if currentLine != "" {
		lines = append(lines, currentLine)
	}
	
	return strings.Join(lines, "\n"), len(lines)
}



func (a *App) populateLeftSections() {
	sections := []string{"Subjects", "Checks", "PDFs", "Skipped", "Warnings", "Errors"}
	var sectionTexts []string
	
	for i, section := range sections {
		var count int
		switch i {
		case 0: // Subjects
			count = len(a.data.Scanned)
			// Add repository if it has issues
			for _, subject := range a.data.DetailsSubjectFocused {
				if subject.Subject == "repository" {
					count++
					break
				}
			}
		case 1: // Checks
			count = len(a.data.DetailsCheckFocused)
		case 2: // PDFs
			count = len(a.data.PDFFiles)
		case 3: // Skipped
			count = len(a.data.Skipped)
		case 4: // Warnings
			count = len(a.data.Warnings)
		case 5: // Errors
			count = len(a.data.Errors)
		}
		
		var sectionText string
		if i == a.selectedLeftPanel {
			sectionText = fmt.Sprintf("[black:white]%s (%d)[-:-]", section, count)
		} else {
			sectionText = fmt.Sprintf("[white]%s (%d)", section, count)
		}
		sectionTexts = append(sectionTexts, sectionText)
	}
	
	// Check if sections fit on one line, otherwise wrap them (same logic as right sections)
	sectionsDisplay, _ := a.formatSectionsResponsive(sectionTexts)
	a.leftSections.SetText(sectionsDisplay)
}

func (a *App) showSubjectsPanel() {
	a.leftContent.Clear()
	a.leftContent.SetDirection(tview.FlexRow).
		AddItem(a.subjectsList, 0, 1, true)
}

func (a *App) showChecksPanel() {
	a.leftContent.Clear()
	a.leftContent.SetDirection(tview.FlexRow).
		AddItem(a.checksList, 0, 1, true)
}

func (a *App) showEmptyLeftPanel(title string) {
	a.leftContent.Clear()
	emptyView := tview.NewTextView().SetDynamicColors(true)
	emptyView.SetText(fmt.Sprintf("[dim]%s[white]\n\n[dim]Details shown in right panel[white]", title))
	emptyView.SetBorder(true).SetTitle(fmt.Sprintf(" %s ", title))
	a.leftContent.SetDirection(tview.FlexRow).
		AddItem(emptyView, 0, 1, true)
}

func (a *App) showSubjectDetails() {
	if a.currentSubject == "" {
		a.detailsContent.SetText("[dim]No subject selected[white]")
		return
	}

	// Find subject details
	for _, subject := range a.data.DetailsSubjectFocused {
		// Match by subject name or by "archive > subject" format
		subjectKey := subject.Subject
		if subject.ArchiveName != "" {
			subjectKey = subject.ArchiveName + " > " + subject.Subject
		}
		if subjectKey == a.currentSubject {
			content := fmt.Sprintf("[yellow]Subject: %s[white]\n", subject.Subject)
			if subject.ArchiveName != "" {
				content += fmt.Sprintf("Archive: %s\n", subject.ArchiveName)
			}
			if subject.Path != "" {
				content += fmt.Sprintf("Path: %s\n", subject.Path)
			}
			content += fmt.Sprintf("\n[green]Issues (%d):[white]\n", len(subject.Issues))

			for i, issue := range subject.Issues {
				content += fmt.Sprintf("\n[cyan]%d. %s[white]\n", i+1, issue.Checkname)
				content += fmt.Sprintf("   %s\n", issue.Message)
			}
			a.detailsContent.SetText(content)
			return
		}
	}

	a.detailsContent.SetText("[dim]No details found[white]")
}

func (a *App) showCheckDetails() {
	if a.currentSubject == "" {
		a.detailsContent.SetText("[dim]No check selected[white]")
		return
	}

	// Find check details
	for _, check := range a.data.DetailsCheckFocused {
		if check.Checkname == a.currentSubject {
			content := fmt.Sprintf("[yellow]Check: %s[white]\n", a.currentSubject)
			content += fmt.Sprintf("\n[green]Issues (%d):[white]\n", len(check.Issues))

			for i, issue := range check.Issues {
				// Show archive context if present
				if issue.ArchiveName != "" {
					content += fmt.Sprintf("\n[cyan]%d. %s > %s[white]\n", i+1, issue.ArchiveName, issue.Subject)
				} else {
					content += fmt.Sprintf("\n[cyan]%d. %s[white]\n", i+1, issue.Subject)
				}
				if issue.Path != "" {
					content += fmt.Sprintf("   Path: %s\n", issue.Path)
				}
				content += fmt.Sprintf("   %s\n", issue.Message)
			}
			a.detailsContent.SetText(content)
			return
		}
	}

	a.detailsContent.SetText("[dim]No details found[white]")
}

func (a *App) showSkippedDetails() {
	content := a.getSkippedContent()
	a.detailsContent.SetText(content)
}

func (a *App) showWarningsDetails() {
	content := a.getWarningsContent()
	a.detailsContent.SetText(content)
}

func (a *App) showErrorsDetails() {
	content := a.getErrorsContent()
	a.detailsContent.SetText(content)
}

func (a *App) showPDFsDetails() {
	content := a.getPDFsContent()
	a.detailsContent.SetText(content)
}

func (a *App) getPDFsContent() string {
	if len(a.data.PDFFiles) == 0 {
		return "[dim]No PDF files found[white]"
	}
	
	content := fmt.Sprintf("[yellow]PDF Files (%d):[white]\n\n", len(a.data.PDFFiles))
	for i, file := range a.data.PDFFiles {
		content += fmt.Sprintf("[cyan]%d.[white] %s\n", i+1, file)
	}
	return content
}

func (a *App) navigateLeftPanelLeft() {
	if a.selectedLeftPanel > 0 {
		a.selectedLeftPanel--
		a.populateLeftSections()
		a.switchToSelectedLeftPanel()
		a.updateControls()
	}
}

func (a *App) navigateLeftPanelRight() {
	if a.selectedLeftPanel < 5 {  // Now we have 6 categories (0-5)
		a.selectedLeftPanel++
		a.populateLeftSections()
		a.switchToSelectedLeftPanel()
		a.updateControls()
	}
}

func (a *App) switchToSelectedLeftPanel() {
	// Reset all colors to white
	a.subjectsList.SetBorderColor(tcell.ColorWhite)
	a.checksList.SetBorderColor(tcell.ColorWhite)
	a.detailsContent.SetBorderColor(tcell.ColorWhite)
	
	// Set navigation header to yellow
	a.leftSections.SetBorderColor(tcell.ColorYellow)
	
	switch a.selectedLeftPanel {
	case 0: // Subjects
		a.currentView = "subjects"
		a.showSubjectsPanel()
		a.app.SetFocus(a.subjectsList)
		a.subjectsList.SetBorderColor(tcell.ColorGreen)
		a.updateDetailsForCurrentSelection()
		
	case 1: // Checks
		a.currentView = "checks"
		a.showChecksPanel()
		a.app.SetFocus(a.checksList)
		a.checksList.SetBorderColor(tcell.ColorGreen)
		a.updateDetailsForCurrentSelection()
		
	case 2: // PDFs
		a.currentView = "pdfs"
		a.showEmptyLeftPanel("PDF Files")
		a.showPDFsDetails()
		a.app.SetFocus(a.detailsContent)
		a.detailsContent.SetBorderColor(tcell.ColorGreen)
		
	case 3: // Skipped
		a.currentView = "skipped"
		a.showEmptyLeftPanel("Skipped Files")
		a.showSkippedDetails()
		a.app.SetFocus(a.detailsContent)
		a.detailsContent.SetBorderColor(tcell.ColorGreen)
		
	case 4: // Warnings
		a.currentView = "warnings"
		a.showEmptyLeftPanel("Warnings")
		a.showWarningsDetails()
		a.app.SetFocus(a.detailsContent)
		a.detailsContent.SetBorderColor(tcell.ColorGreen)
		
	case 5: // Errors
		a.currentView = "errors"
		a.showEmptyLeftPanel("Errors")
		a.showErrorsDetails()
		a.app.SetFocus(a.detailsContent)
		a.detailsContent.SetBorderColor(tcell.ColorGreen)
	}
}

func (a *App) updateDetailsForCurrentSelection() {
	// Get the currently selected item from the active list
	if a.currentView == "subjects" {
		currentIndex := a.subjectsList.GetCurrentItem()
		if currentIndex >= 0 {
			// Get subject name based on index
			if currentIndex < len(a.data.Scanned) {
				a.currentSubject = a.data.Scanned[currentIndex].Filename
			} else {
				// Must be repository
				a.currentSubject = "repository"
			}
			// Update details panel with selected subject
			a.showSubjectDetails()
		}
	} else if a.currentView == "checks" {
		currentIndex := a.checksList.GetCurrentItem()
		if currentIndex >= 0 && currentIndex < len(a.data.DetailsCheckFocused) {
			a.currentSubject = a.data.DetailsCheckFocused[currentIndex].Checkname
			// Update details panel with selected check
			a.showCheckDetails()
		}
	}
}

func (a *App) getSkippedContent() string {
	if len(a.data.Skipped) == 0 {
		return "[dim]No skipped files[white]"
	}
	
	content := fmt.Sprintf("[yellow]Skipped Files (%d):[white]\n\n", len(a.data.Skipped))
	for i, file := range a.data.Skipped {
		content += fmt.Sprintf("[cyan]%d.[white] %s\n", i+1, file.Filename)
		if file.Path != "" {
			content += fmt.Sprintf("   [dim]Path: %s[white]\n", file.Path)
		}
		content += fmt.Sprintf("   [dim]Reason: %s[white]\n\n", file.Reason)
	}
	return content
}

func (a *App) getWarningsContent() string {
	if len(a.data.Warnings) == 0 {
		return "[dim]No warnings[white]"
	}
	
	content := fmt.Sprintf("[yellow]Warnings (%d):[white]\n\n", len(a.data.Warnings))
	for i, warning := range a.data.Warnings {
		content += fmt.Sprintf("[yellow]%d.[white] [%s] %s\n", i+1, warning.Timestamp, warning.Message)
	}
	return content
}

func (a *App) getErrorsContent() string {
	if len(a.data.Errors) == 0 {
		return "[dim]No errors[white]"
	}
	
	content := fmt.Sprintf("[red]Errors (%d):[white]\n\n", len(a.data.Errors))
	for i, err := range a.data.Errors {
		content += fmt.Sprintf("[red]%d.[white] [%s] %s\n", i+1, err.Timestamp, err.Message)
	}
	return content
}



func (a *App) ShowProgressBar() {
	if !a.isScanning {
		a.isScanning = true
		a.progressBar.SetText("Initializing scan...")
		// Progress bar is always part of layout, just show it
		a.app.QueueUpdateDraw(func() {})
	}
}

func (a *App) HideProgressBar() {
	if a.isScanning {
		a.isScanning = false
		a.progressBar.SetText("")
		a.app.QueueUpdateDraw(func() {})
	}
}

func (a *App) UpdateProgress(current, total int, message string) {
	if total == 0 {
		a.progressBar.SetText("Initializing scan...")
		a.app.QueueUpdateDraw(func() {})
		return
	}
	
	// Ensure current doesn't exceed total
	if current > total {
		current = total
	}
	
	percentage := float64(current) / float64(total) * 100
	barWidth := 40 // Width of the progress bar (shorter to fit more text)
	filledWidth := int(float64(barWidth) * float64(current) / float64(total))
	
	// Create progress bar visual
	bar := ""
	for i := 0; i < barWidth; i++ {
		if i < filledWidth {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	
	// Use different colors for completed vs in-progress
	var progressText string
	if current == total && current > 0 {
		// Scan completed - show green
		progressText = fmt.Sprintf("[yellow]Progress:[white] %d/%d (%.1f%%) [green]%s[white] [green]%s[white]", 
			current, total, percentage, bar, message)
	} else {
		// Scan in progress - normal colors
		progressText = fmt.Sprintf("[yellow]Progress:[white] %d/%d (%.1f%%) [green]%s[white] %s", 
			current, total, percentage, bar, message)
	}
	
	a.progressBar.SetText(progressText)
	a.app.QueueUpdateDraw(func() {})
}

func (a *App) UpdateData(newData *ScanResult) {
	a.data = newData
	a.populateSubjectsList()
	a.populateChecksList()
	a.populateLeftSections() // Update navigation counts
	a.updateInfo()
	
	// Auto-select first subject if available
	a.autoSelectFirstSubject()
	
	// Focus the navigation panel so user can immediately start navigating
	a.focusSubjects()
	
	a.app.QueueUpdateDraw(func() {})
}

func (a *App) autoSelectFirstSubject() {
	// Auto-select first subject if any subjects are available
	if len(a.data.Scanned) > 0 {
		// Select first scanned file
		firstFile := a.data.Scanned[0]
		a.currentSubject = firstFile.Filename
		a.subjectsList.SetCurrentItem(0)
		// Explicitly update details for the selected subject
		a.showSubjectDetails()
	} else {
		// Check if repository has issues and select it
		for _, subject := range a.data.DetailsSubjectFocused {
			if subject.Subject == "repository" {
				a.currentSubject = "repository"
				a.subjectsList.SetCurrentItem(0)
				// Explicitly update details for the selected subject
				a.showSubjectDetails()
				break
			}
		}
	}
}

func (a *App) SetStartupCallback(callback func()) {
	a.startupCallback = callback
}

func (a *App) Run() error {
	// Start the startup callback after a brief delay to ensure TUI is ready
	if a.startupCallback != nil {
		go func() {
			// Small delay to ensure TUI event loop is started
			time.Sleep(50 * time.Millisecond)
			a.startupCallback()
		}()
	}
	return a.app.Run()
}

// SetLocation sets the location/path being scanned (used in summary)
func (a *App) SetLocation(location string) {
	a.location = location
}

// setupSummaryModal creates the modal overlay for the copy-paste summary
func (a *App) setupSummaryModal() {
	// Create the text view for summary content
	a.summaryTextView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true)
	a.summaryTextView.SetBorder(true).SetTitle(" Summary (copied to clipboard) ")

	// Create instructions text
	instructions := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[yellow]Press ESC or X to close[white]  |  [yellow]↑↓[white] to scroll")
	instructions.SetTextAlign(tview.AlignCenter)

	// Create the modal container
	innerFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.summaryTextView, 0, 1, true).
		AddItem(instructions, 1, 0, false)
	innerFlex.SetBorder(true).SetTitle(" Copy-Paste Summary ")
	innerFlex.SetBorderColor(tcell.ColorYellow)

	// Create centered modal with padding
	a.summaryModal = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 2, 0, false).  // Top padding
		AddItem(tview.NewFlex().
			AddItem(nil, 4, 0, false).  // Left padding
			AddItem(innerFlex, 0, 1, true).
			AddItem(nil, 4, 0, false),  // Right padding
		0, 1, true).
		AddItem(nil, 2, 0, false)  // Bottom padding
}

// showSummaryModal generates the summary, copies to clipboard, and shows the modal
func (a *App) showSummaryModal() {
	if a.summaryVisible {
		return
	}

	// Generate the summary
	generator := NewSummaryGenerator(a.data, a.location)
	summary := generator.Generate()

	// Try to copy to clipboard
	clipboardStatus := ""
	if err := clipboard.WriteAll(summary); err != nil {
		// Fallback to OSC 52 for remote/tmux environments
		if osc52Err := copyToClipboardOSC52(summary); osc52Err != nil {
			clipboardStatus = "\n\n[red]Note: Could not copy to clipboard: " + err.Error() + "[white]"
			a.summaryTextView.SetTitle(" Summary (clipboard unavailable) ")
		} else {
			clipboardStatus = "\n\n[yellow]Note: Used OSC 52 for clipboard (works if terminal supports it)[white]"
			a.summaryTextView.SetTitle(" Summary (OSC 52 clipboard) ")
		}
	} else {
		a.summaryTextView.SetTitle(" Summary (copied to clipboard) ")
	}

	// Set the summary text
	a.summaryTextView.SetText(summary + clipboardStatus)
	a.summaryTextView.ScrollToBeginning()

	// Show the modal by replacing the root
	a.summaryVisible = true
	a.app.SetRoot(a.summaryModal, true)
	a.app.SetFocus(a.summaryTextView)
}

// hideSummaryModal hides the modal and returns to the main view
func (a *App) hideSummaryModal() {
	if !a.summaryVisible {
		return
	}

	a.summaryVisible = false
	a.app.SetRoot(a.flex, true)

	// Restore focus to previous view
	switch a.currentView {
	case "subjects":
		a.app.SetFocus(a.subjectsList)
	case "checks":
		a.app.SetFocus(a.checksList)
	case "details":
		a.app.SetFocus(a.detailsContent)
	default:
		a.app.SetFocus(a.subjectsList)
	}
}