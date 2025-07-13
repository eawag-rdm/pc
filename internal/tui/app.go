package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type App struct {
	app               *tview.Application
	data              *ScanResult
	subjectsList      *tview.List
	checksList        *tview.List
	leftSections      *tview.TextView // Header bar for Subjects/Checks switching
	leftContent       *tview.Flex     // Content area for subjects or checks list
	detailsSections   *tview.TextView // New: collapsible sections display
	detailsContent    *tview.TextView // Content for selected section
	info              *tview.TextView
	controls          *tview.TextView
	progressBar       *tview.TextView // Progress bar for scanning
	flex              *tview.Flex
	leftPanel         *tview.Flex     // Store reference to left panel for dynamic content
	rightPanel        *tview.Flex     // Store reference to right panel for dynamic height
	pages             *tview.Pages
	currentView       string // "subjects", "checks", or "details"
	currentSubject    string // Currently selected subject/check
	selectedSection   int    // Currently selected details section (0-3)
	selectedLeftPanel int    // Currently selected left panel (0=subjects, 1=checks)
	isScanning        bool   // Whether we're currently scanning
	startupCallback   func() // Called when TUI starts running
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
		Errors:                []LogMessage{},
		Warnings:              []LogMessage{},
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
	a.detailsSections = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
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
	a.detailsSections.SetBorder(true).SetTitle(" Sections ")
	a.detailsContent.SetBorder(true).SetTitle(" Content ")
	a.info.SetBorder(true).SetTitle(" Summary ")
	a.controls.SetBorder(true).SetTitle(" Controls ")
	a.progressBar.SetBorder(true).SetTitle(" Scan Progress ")

	// Create left panel with sections header and content
	a.leftPanel = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.leftSections, 3, 0, false).
		AddItem(a.leftContent, 0, 1, true)

	a.rightPanel = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.info, 6, 0, false).
		AddItem(a.detailsSections, 4, 0, false).
		AddItem(a.detailsContent, 0, 1, false)

	mainContent := tview.NewFlex().
		AddItem(a.leftPanel, 0, 1, true).
		AddItem(a.rightPanel, 0, 2, false)

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
	a.populateSections()
	a.updateInfo()
	a.updateControls()

	// Set up key bindings
	a.setupKeyBindings()

	// Set up resize handler for responsive sections
	a.setupResizeHandler()

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
			// Update current subject and refresh sections without changing focus
			a.currentSubject = subjectNames[index]
			a.populateSections()
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
			// Update current subject and refresh sections without changing focus
			a.currentSubject = checkNames[index]
			a.populateSections()
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
	if a.currentView == "details" {
		controls = "[yellow]TAB[white]=Switch  [yellow]←→[white]=Sections  [yellow]↑↓[white]=Scroll  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]H[white]=Help  [yellow]Q[white]=Quit"
	} else {
		controls = "[yellow]TAB[white]=Switch  [yellow]←→[white]=Panel  [yellow]↑↓[white]=Navigate  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]H[white]=Help  [yellow]Q[white]=Quit"
	}
	
	a.controls.SetText(controls)
}

func (a *App) showSubjectDetails(subject string) {
	// Find the subject details
	for _, subjectDetail := range a.data.DetailsSubjectFocused {
		if subjectDetail.Subject == subject {
			content := fmt.Sprintf("[yellow]Subject: %s[white]\n", subject)
			if subjectDetail.Path != "" {
				content += fmt.Sprintf("Path: %s\n", subjectDetail.Path)
			}
			content += fmt.Sprintf("\n[green]Issues (%d):[white]\n", len(subjectDetail.Issues))
			
			for i, issue := range subjectDetail.Issues {
				content += fmt.Sprintf("\n[cyan]%d. %s[white]\n", i+1, issue.Checkname)
				content += fmt.Sprintf("   %s\n", issue.Message)
			}
			
			a.currentSubject = subject
			a.currentView = "details"
			a.populateSections()
			a.focusDetails()
			return
		}
	}
}

func (a *App) showCheckDetails(checkname string) {
	// Find the check details
	for _, check := range a.data.DetailsCheckFocused {
		if check.Checkname == checkname {
			content := fmt.Sprintf("[yellow]Check: %s[white]\n", checkname)
			content += fmt.Sprintf("\n[green]Issues (%d):[white]\n", len(check.Issues))
			
			for i, issue := range check.Issues {
				content += fmt.Sprintf("\n[cyan]%d. %s[white]\n", i+1, issue.Subject)
				if issue.Path != "" {
					content += fmt.Sprintf("   Path: %s\n", issue.Path)
				}
				content += fmt.Sprintf("   %s\n", issue.Message)
			}
			
			a.currentSubject = checkname
			a.currentView = "details"
			a.populateSections()
			a.focusDetails()
			return
		}
	}
}

func (a *App) setupResizeHandler() {
	// Set up a periodic refresh to check for size changes
	// This will handle terminal resize events
	go func() {
		lastWidth := 0
		for {
			time.Sleep(100 * time.Millisecond)
			
			// Check if the width has changed
			_, _, currentWidth, _ := a.detailsSections.GetRect()
			if currentWidth > 0 && currentWidth != lastWidth {
				lastWidth = currentWidth
				a.app.QueueUpdateDraw(func() {
					a.populateSections()
				})
			}
		}
	}()
}

func (a *App) setupKeyBindings() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
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
		case 'h', 'H':
			a.showHelp()
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
		}
		
		// Handle arrow keys for navigation
		switch event.Key() {
		case tcell.KeyLeft:
			if a.currentView == "details" {
				a.navigateSectionsLeft()
			} else if a.currentView == "subjects" || a.currentView == "checks" {
				a.navigateLeftPanelLeft()
			}
			return nil
		case tcell.KeyRight:
			if a.currentView == "details" {
				a.navigateSectionsRight()
			} else if a.currentView == "subjects" || a.currentView == "checks" {
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
	a.detailsSections.SetBorderColor(tcell.ColorWhite)
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
	a.detailsSections.SetBorderColor(tcell.ColorWhite)
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
	a.detailsSections.SetBorderColor(tcell.ColorYellow)
	a.detailsContent.SetBorderColor(tcell.ColorGreen)
	a.updateControls()
}

func (a *App) formatSectionsResponsive(sectionTexts []string) (string, int) {
	// Get the terminal width for the sections area
	// Use multiple methods to get the most accurate width
	_, _, width, _ := a.detailsSections.GetRect()
	
	// If the component doesn't have a width yet, use a reasonable default
	if width <= 0 {
		width = 80 // Default terminal width estimate
	}
	
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

func (a *App) populateSections() {
	// Section names
	sections := []string{"Details", "PDF Files", "Warnings", "Errors", "Skipped"}
	var sectionTexts []string
	
	for i, section := range sections {
		var count int
		switch i {
		case 0: // Details - count issues for current subject
			count = a.getDetailsCount()
		case 1: // PDF Files
			count = len(a.data.PDFFiles)
		case 2: // Warnings
			count = len(a.data.Warnings)
		case 3: // Errors
			count = len(a.data.Errors)
		case 4: // Skipped
			count = len(a.data.Skipped)
		}
		
		// Format with selection highlighting (no toggle indicators)
		var sectionText string
		if i == a.selectedSection {
			sectionText = fmt.Sprintf("[black:white]%s (%d)[-:-]", section, count)
		} else {
			sectionText = fmt.Sprintf("[white]%s (%d)", section, count)
		}
		sectionTexts = append(sectionTexts, sectionText)
	}
	
	// Check if sections fit on one line, otherwise wrap them
	sectionsDisplay, lineCount := a.formatSectionsResponsive(sectionTexts)
	a.detailsSections.SetText(sectionsDisplay)
	
	// Dynamically adjust the height of the sections area
	a.adjustSectionsHeight(lineCount)
	
	a.updateSectionContent()
}

func (a *App) adjustSectionsHeight(lineCount int) {
	// Calculate the required height (content lines + border)
	requiredHeight := lineCount + 2 // +2 for top and bottom borders
	
	// Ensure minimum height of 3 and maximum of 6 to prevent layout issues
	if requiredHeight < 3 {
		requiredHeight = 3
	}
	if requiredHeight > 6 {
		requiredHeight = 6
	}
	
	// Rebuild the right panel with the new height
	a.rightPanel.Clear()
	a.rightPanel.SetDirection(tview.FlexRow).
		AddItem(a.info, 6, 0, false).
		AddItem(a.detailsSections, requiredHeight, 0, false).
		AddItem(a.detailsContent, 0, 1, false)
}

func (a *App) populateLeftSections() {
	sections := []string{"Subjects", "Checks"}
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

func (a *App) navigateLeftPanelLeft() {
	if a.selectedLeftPanel > 0 {
		a.selectedLeftPanel--
		a.populateLeftSections()
		if a.selectedLeftPanel == 0 {
			a.currentView = "subjects"
			a.showSubjectsPanel()
			a.app.SetFocus(a.subjectsList)
			// Set colors: left navigation header = yellow, subjects list = green, others = white
			a.leftSections.SetBorderColor(tcell.ColorYellow)
			a.subjectsList.SetBorderColor(tcell.ColorGreen)
			a.checksList.SetBorderColor(tcell.ColorWhite)
			a.detailsSections.SetBorderColor(tcell.ColorWhite)
			a.detailsContent.SetBorderColor(tcell.ColorWhite)
			// Update details for first subject
			a.updateDetailsForCurrentSelection()
		} else {
			a.currentView = "checks"
			a.showChecksPanel()
			a.app.SetFocus(a.checksList)
			// Set colors: left navigation header = yellow, checks list = green, others = white
			a.leftSections.SetBorderColor(tcell.ColorYellow)
			a.subjectsList.SetBorderColor(tcell.ColorWhite)
			a.checksList.SetBorderColor(tcell.ColorGreen)
			a.detailsSections.SetBorderColor(tcell.ColorWhite)
			a.detailsContent.SetBorderColor(tcell.ColorWhite)
			// Update details for first check
			a.updateDetailsForCurrentSelection()
		}
		a.updateControls()
	}
}

func (a *App) navigateLeftPanelRight() {
	if a.selectedLeftPanel < 1 {
		a.selectedLeftPanel++
		a.populateLeftSections()
		if a.selectedLeftPanel == 0 {
			a.currentView = "subjects"
			a.showSubjectsPanel()
			a.app.SetFocus(a.subjectsList)
			// Set colors: left navigation header = yellow, subjects list = green, others = white
			a.leftSections.SetBorderColor(tcell.ColorYellow)
			a.subjectsList.SetBorderColor(tcell.ColorGreen)
			a.checksList.SetBorderColor(tcell.ColorWhite)
			a.detailsSections.SetBorderColor(tcell.ColorWhite)
			a.detailsContent.SetBorderColor(tcell.ColorWhite)
			// Update details for first subject
			a.updateDetailsForCurrentSelection()
		} else {
			a.currentView = "checks"
			a.showChecksPanel()
			a.app.SetFocus(a.checksList)
			// Set colors: left navigation header = yellow, checks list = green, others = white
			a.leftSections.SetBorderColor(tcell.ColorYellow)
			a.subjectsList.SetBorderColor(tcell.ColorWhite)
			a.checksList.SetBorderColor(tcell.ColorGreen)
			a.detailsSections.SetBorderColor(tcell.ColorWhite)
			a.detailsContent.SetBorderColor(tcell.ColorWhite)
			// Update details for first check
			a.updateDetailsForCurrentSelection()
		}
		a.updateControls()
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
		}
	} else if a.currentView == "checks" {
		currentIndex := a.checksList.GetCurrentItem()
		if currentIndex >= 0 && currentIndex < len(a.data.DetailsCheckFocused) {
			a.currentSubject = a.data.DetailsCheckFocused[currentIndex].Checkname
		}
	}
	
	// Update sections with the new selection
	a.populateSections()
}

func (a *App) getDetailsCount() int {
	if a.currentSubject == "" {
		return 0
	}
	
	// Count issues for current subject
	for _, subject := range a.data.DetailsSubjectFocused {
		if subject.Subject == a.currentSubject {
			return len(subject.Issues)
		}
	}
	
	// Check in check-focused data
	for _, check := range a.data.DetailsCheckFocused {
		if check.Checkname == a.currentSubject {
			return len(check.Issues)
		}
	}
	
	return 0
}


func (a *App) navigateSectionsLeft() {
	if a.selectedSection > 0 {
		a.selectedSection--
		a.populateSections()
	}
}

func (a *App) navigateSectionsRight() {
	if a.selectedSection < 4 { // 0-4 for 5 sections
		a.selectedSection++
		a.populateSections()
	}
}

func (a *App) updateSectionContent() {
	if a.selectedSection < 0 || a.selectedSection >= 5 {
		a.detailsContent.SetText("[dim]Select a section to view content[white]")
		return
	}
	
	var content string
	switch a.selectedSection {
	case 0: // Details
		content = a.getDetailsContent()
	case 1: // PDF Files
		content = a.getPDFFilesContent()
	case 2: // Warnings
		content = a.getWarningsContent()
	case 3: // Errors
		content = a.getErrorsContent()
	case 4: // Skipped
		content = a.getSkippedContent()
	}
	
	a.detailsContent.SetText(content)
}

func (a *App) getDetailsContent() string {
	if a.currentSubject == "" {
		return "[dim]No subject selected[white]"
	}
	
	// Find subject details
	for _, subject := range a.data.DetailsSubjectFocused {
		if subject.Subject == a.currentSubject {
			content := fmt.Sprintf("[yellow]Subject: %s[white]\n", a.currentSubject)
			if subject.Path != "" {
				content += fmt.Sprintf("Path: %s\n", subject.Path)
			}
			content += fmt.Sprintf("\n[green]Issues (%d):[white]\n", len(subject.Issues))
			
			for i, issue := range subject.Issues {
				content += fmt.Sprintf("\n[cyan]%d. %s[white]\n", i+1, issue.Checkname)
				content += fmt.Sprintf("   %s\n", issue.Message)
			}
			return content
		}
	}
	
	// Check in check-focused data
	for _, check := range a.data.DetailsCheckFocused {
		if check.Checkname == a.currentSubject {
			content := fmt.Sprintf("[yellow]Check: %s[white]\n", a.currentSubject)
			content += fmt.Sprintf("\n[green]Issues (%d):[white]\n", len(check.Issues))
			
			for i, issue := range check.Issues {
				content += fmt.Sprintf("\n[cyan]%d. %s[white]\n", i+1, issue.Subject)
				if issue.Path != "" {
					content += fmt.Sprintf("   Path: %s\n", issue.Path)
				}
				content += fmt.Sprintf("   %s\n", issue.Message)
			}
			return content
		}
	}
	
	return "[dim]No details found[white]"
}

func (a *App) getPDFFilesContent() string {
	if len(a.data.PDFFiles) == 0 {
		return "[dim]No PDF files found[white]"
	}
	
	content := fmt.Sprintf("[yellow]PDF Files (%d):[white]\n\n", len(a.data.PDFFiles))
	for i, file := range a.data.PDFFiles {
		content += fmt.Sprintf("[cyan]%d.[white] %s\n", i+1, file)
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

func (a *App) showHelp() {
	helpText := fmt.Sprintf(`[yellow]PC Scanner TUI - Help[white]

[green]Navigation:[white]
  %-12s %s
  %-12s %s
  %-12s %s
  %-12s %s
  %-12s %s
  
[green]Shortcuts:[white]
  %-12s %s
  %-12s %s
  %-12s %s
  %-12s %s
  %-12s %s

[green]Layout:[white]
  %-12s %s
  %-12s %s

[green]Details Sections:[white]
  %-12s %s
  %-12s %s
  %-12s %s
  %-12s %s
  %-12s %s

[green]Content Scrolling:[white]
  When focused on the content area, use ↑/↓ to scroll through content.
  Long lists and text will scroll automatically.

[yellow]Press any key to close help[white]`,
		"Tab", "Switch between left panel and content area",
		"↑/↓", "Navigate within panels / Scroll content",
		"←/→", "Switch panels (Subjects/Checks) or sections (Details)",
		"s/S", "Focus Subjects panel",
		"c/C", "Focus Checks panel",
		"d/D", "Focus Details panel (when available)",
		"h/H", "Show this help",
		"q/Q or Esc", "Quit application",
		"Left Panel", "Focused on header + selected content (Subjects or Checks)",
		"Right Panel", "Summary (top) / Sections (middle) / Content (bottom)",
		"Details", "Issues for selected subject/check",
		"PDF Files", "List of PDF files found during scan",
		"Warnings", "Warning messages from scan",
		"Errors", "Error messages from scan",
		"Skipped", "Files skipped during scan with reasons")

	modal := tview.NewModal().
		SetText(helpText).
		AddButtons([]string{"Close"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.app.SetRoot(a.flex, true)
		})

	a.app.SetRoot(modal, true)
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
	a.populateSections()
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
		a.populateSections()
	} else {
		// Check if repository has issues and select it
		for _, subject := range a.data.DetailsSubjectFocused {
			if subject.Subject == "repository" {
				a.currentSubject = "repository"
				a.subjectsList.SetCurrentItem(0)
				a.populateSections()
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