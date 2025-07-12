package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type App struct {
	app          *tview.Application
	data         *ScanResult
	subjectsList *tview.List
	checksList   *tview.List
	details      *tview.TextView
	info         *tview.TextView
	controls     *tview.TextView
	flex         *tview.Flex
	pages        *tview.Pages
	currentView  string // "subjects" or "checks"
}

func NewApp(data *ScanResult) *App {
	app := &App{
		app:         tview.NewApplication(),
		data:        data,
		currentView: "subjects",
	}
	app.setupUI()
	return app
}

func (a *App) setupUI() {
	// Create components
	a.subjectsList = tview.NewList().ShowSecondaryText(true)
	a.checksList = tview.NewList().ShowSecondaryText(true)
	a.details = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	a.info = tview.NewTextView().SetDynamicColors(true)
	a.controls = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)

	// Set up borders and titles
	a.subjectsList.SetBorder(true).SetTitle(" Subjects ")
	a.checksList.SetBorder(true).SetTitle(" Checks ")
	a.details.SetBorder(true).SetTitle(" Details ")
	a.info.SetBorder(true).SetTitle(" Summary ")
	a.controls.SetBorder(true).SetTitle(" Controls ")

	// Create main layout
	leftPanel := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.subjectsList, 0, 1, true).
		AddItem(a.checksList, 0, 1, false)

	rightPanel := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.info, 6, 0, false).
		AddItem(a.details, 0, 1, false)

	mainContent := tview.NewFlex().
		AddItem(leftPanel, 0, 1, true).
		AddItem(rightPanel, 0, 2, false)

	a.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.controls, 3, 0, false).
		AddItem(mainContent, 0, 1, false)

	// Populate data
	a.populateSubjectsList()
	a.populateChecksList()
	a.updateInfo()
	a.updateControls()

	// Set up key bindings
	a.setupKeyBindings()

	// Set root
	a.app.SetRoot(a.flex, true)
}

func (a *App) populateSubjectsList() {
	a.subjectsList.Clear()
	
	// Add scanned files
	for _, file := range a.data.Scanned {
		issueCount := 0
		checkNames := []string{}
		for _, issue := range file.Issues {
			issueCount += issue.IssueCount
			checkNames = append(checkNames, issue.Checkname)
		}
		
		mainText := fmt.Sprintf("%s (%d)", file.Filename, issueCount)
		secondaryText := strings.Join(checkNames, ", ")
		
		// Capture file for closure
		currentFile := file
		a.subjectsList.AddItem(mainText, secondaryText, 0, func() {
			a.showSubjectDetails(currentFile.Filename)
		})
	}

	// Add repository if it has issues
	for _, subject := range a.data.DetailsSubjectFocused {
		if subject.Subject == "repository" {
			issueCount := len(subject.Issues)
			checkNames := []string{}
			for _, issue := range subject.Issues {
				checkNames = append(checkNames, issue.Checkname)
			}
			
			mainText := fmt.Sprintf("repository (%d)", issueCount)
			secondaryText := strings.Join(checkNames, ", ")
			
			a.subjectsList.AddItem(mainText, secondaryText, 0, func() {
				a.showSubjectDetails("repository")
			})
			break
		}
	}
}

func (a *App) populateChecksList() {
	a.checksList.Clear()
	
	for _, check := range a.data.DetailsCheckFocused {
		issueCount := len(check.Issues)
		subjects := []string{}
		for _, issue := range check.Issues {
			subjects = append(subjects, issue.Subject)
		}
		
		mainText := fmt.Sprintf("%s (%d)", check.Checkname, issueCount)
		secondaryText := fmt.Sprintf("%d subjects", len(subjects))
		
		// Capture check for closure
		currentCheck := check
		a.checksList.AddItem(mainText, secondaryText, 0, func() {
			a.showCheckDetails(currentCheck.Checkname)
		})
	}
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
		"Subjects Scanned: %d | Subjects Skipped: %d\n"+
		"Issues Found: %d | Subjects with Issues: %d\n"+
		"Errors: %d | Warnings: %d",
		a.data.Timestamp,
		totalScanned,
		totalSkipped,
		totalIssues,
		len(a.data.Scanned),
		len(a.data.Errors),
		len(a.data.Warnings),
	)
	
	a.info.SetText(info)
}

func (a *App) updateControls() {
	// Count warnings and errors for display
	errorCount := len(a.data.Errors)
	warningCount := len(a.data.Warnings)
	
	// Build status part with proper color coding
	status := ""
	if errorCount > 0 || warningCount > 0 {
		errorPart := ""
		warningPart := ""
		
		if errorCount > 0 {
			errorPart = fmt.Sprintf("Errors: [red]%d[white]", errorCount)
		}
		if warningCount > 0 {
			warningPart = fmt.Sprintf("Warnings: [yellow]%d[white]", warningCount)
		}
		
		if errorPart != "" && warningPart != "" {
			status = fmt.Sprintf("  %s  %s  |  ", errorPart, warningPart)
		} else if errorPart != "" {
			status = fmt.Sprintf("  %s  |  ", errorPart)
		} else if warningPart != "" {
			status = fmt.Sprintf("  %s  |  ", warningPart)
		}
	}
	
	// Concise controls that fit in standard terminal width
	controls := status + "[yellow]TAB[white]=Switch  [yellow]↑↓[white]=Navigate  [yellow]ENTER[white]=Details  [yellow]S[white]=Subjects  [yellow]C[white]=Checks  [yellow]H[white]=Help  [yellow]Q[white]=Quit"
	
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
			
			a.details.SetText(content)
			a.details.SetTitle(fmt.Sprintf(" Details: %s ", subject))
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
			
			a.details.SetText(content)
			a.details.SetTitle(fmt.Sprintf(" Details: %s ", checkname))
			return
		}
	}
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
		}
		
		return event
	})
}

func (a *App) switchFocus() {
	if a.currentView == "subjects" {
		a.focusChecks()
	} else {
		a.focusSubjects()
	}
}

func (a *App) focusSubjects() {
	a.currentView = "subjects"
	a.app.SetFocus(a.subjectsList)
	a.subjectsList.SetBorderColor(tcell.ColorGreen)
	a.checksList.SetBorderColor(tcell.ColorWhite)
}

func (a *App) focusChecks() {
	a.currentView = "checks"
	a.app.SetFocus(a.checksList)
	a.checksList.SetBorderColor(tcell.ColorGreen)
	a.subjectsList.SetBorderColor(tcell.ColorWhite)
}

func (a *App) showHelp() {
	helpText := `[yellow]PC Scanner TUI - Help[white]

[green]Navigation:[white]
  Tab           - Switch between Subjects/Checks panels
  ↑/↓           - Navigate within panels
  Enter         - View details for selected item
  
[green]Shortcuts:[white]
  s/S           - Focus Subjects panel
  c/C           - Focus Checks panel
  h/H           - Show this help
  q/Q or Esc    - Quit application

[green]Panels:[white]
  Subjects      - Shows all scanned subjects with issue counts
  Checks        - Shows all check types with affected subject counts
  Details       - Shows detailed information for selected item
  Summary       - Shows overall scan statistics
  Controls      - Shows navigation shortcuts

[yellow]Press any key to close help[white]`

	modal := tview.NewModal().
		SetText(helpText).
		AddButtons([]string{"Close"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.app.SetRoot(a.flex, true)
		})

	a.app.SetRoot(modal, true)
}

func (a *App) Run() error {
	return a.app.Run()
}