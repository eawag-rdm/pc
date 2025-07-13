package html

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"
)

// HTMLFormatter handles generation of static HTML reports
type HTMLFormatter struct{}

// NewHTMLFormatter creates a new HTML formatter instance
func NewHTMLFormatter() *HTMLFormatter {
	return &HTMLFormatter{}
}

// GenerateReport creates a static HTML file from the scan results
func (h *HTMLFormatter) GenerateReport(jsonData string, outputPath string) error {
	// Parse the JSON data to extract summary information
	var scanResult map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &scanResult); err != nil {
		return fmt.Errorf("failed to parse JSON data: %w", err)
	}


	// Prepare template data - we need to pass the parsed JSON object, not the string
	templateData := struct {
		JSONData    template.JS
		GeneratedAt string
		Title       string
	}{
		JSONData:    template.JS(jsonData), // Use template.JS to safely embed JSON
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		Title:       "Package Checker Scanner Report",
	}

	// Create the HTML template
	tmpl := template.Must(template.New("report").Parse(htmlTemplate))

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create the output file
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create HTML file: %w", err)
	}
	defer file.Close()

	// Execute the template
	if err := tmpl.Execute(file, templateData); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

// HTML template with embedded CSS and JavaScript
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
    <style>
        :root {
            --primary-color: #035C77;
            --primary-light: #118EC6;
            --secondary-color: #64748b;
            --success-color: #10b981;
            --warning-color: #f59e0b;
            --error-color: #ef4444;
            --background-color: #ffffff;
            --surface-color: #f8fafc;
            --text-color: #1e293b;
            --text-secondary: #64748b;
            --border-color: #e2e8f0;
            --shadow: 0 1px 3px 0 rgba(0, 0, 0, 0.1);
            --sidebar-width: 280px;
            --eawag-primary: #035C77;
            --eawag-accent: #118EC6;
        }

        [data-theme="dark"] {
            --primary-color: #118EC6;
            --primary-light: #35A5D1;
            --background-color: #0f172a;
            --surface-color: #1e293b;
            --text-color: #f1f5f9;
            --text-secondary: #94a3b8;
            --border-color: #334155;
            --shadow: 0 1px 3px 0 rgba(0, 0, 0, 0.3);
            --eawag-primary: #118EC6;
            --eawag-accent: #35A5D1;
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background-color: var(--background-color);
            color: var(--text-color);
            line-height: 1.4;
            font-size: 13px;
            transition: background-color 0.3s, color 0.3s;
        }

        .app-layout {
            display: flex;
            height: 100vh;
        }

        .sidebar {
            width: var(--sidebar-width);
            background: var(--surface-color);
            border-right: 1px solid var(--border-color);
            display: flex;
            flex-direction: column;
            box-shadow: var(--shadow);
        }

        .main-content {
            flex: 1;
            display: flex;
            flex-direction: column;
            overflow: hidden;
        }

        .header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 12px 20px;
            background: var(--surface-color);
            border-bottom: 1px solid var(--border-color);
        }

        .header h1 {
            color: var(--primary-color);
            font-size: 1.5rem;
            font-weight: 600;
        }

        .header-controls {
            display: flex;
            gap: 12px;
            align-items: center;
        }

        .theme-toggle {
            background: var(--primary-color);
            color: white;
            border: none;
            padding: 6px 12px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 12px;
            transition: background-color 0.2s ease;
        }

        .theme-toggle:hover {
            background: var(--primary-light);
        }

        .filter-box {
            padding: 6px 10px;
            border: 1px solid var(--border-color);
            border-radius: 4px;
            background: var(--background-color);
            color: var(--text-color);
            width: 200px;
            font-size: 12px;
        }

        .stats-bar {
            display: flex;
            gap: 10px;
            padding: 8px 20px;
            background: var(--background-color);
            border-bottom: 1px solid var(--border-color);
            font-size: 11px;
        }

        .stat-item {
            display: flex;
            align-items: center;
            gap: 4px;
            padding: 4px 8px;
            background: var(--surface-color);
            border-radius: 4px;
        }

        .stat-number {
            font-weight: 600;
        }

        .scanned { color: var(--success-color); }
        .issues { color: var(--error-color); }
        .skipped { color: var(--warning-color); }
        .warnings { color: var(--warning-color); }
        .errors { color: var(--error-color); }

        .sidebar-header {
            padding: 15px;
            border-bottom: 1px solid var(--border-color);
            font-weight: 600;
            font-size: 14px;
        }

        .navigation {
            flex: 1;
            overflow-y: auto;
        }

        .nav-section {
            border-bottom: 1px solid var(--border-color);
        }

        .nav-section-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 12px 15px;
            cursor: pointer;
            background: var(--background-color);
            transition: background-color 0.2s;
            font-size: 12px;
            font-weight: 600;
        }

        .nav-section-header:hover {
            background: var(--border-color);
        }

        .nav-section-header.active {
            background: var(--primary-color);
            color: white;
        }

        .nav-section-count {
            background: var(--border-color);
            color: var(--text-color);
            padding: 2px 6px;
            border-radius: 3px;
            font-size: 10px;
        }

        .nav-section-header.active .nav-section-count {
            background: rgba(255, 255, 255, 0.2);
            color: white;
        }

        .nav-section-content {
            display: none;
            max-height: 300px;
            overflow-y: auto;
        }

        .nav-section-content:focus {
            outline: none;
        }

        .nav-section-content.expanded {
            display: block;
        }

        .nav-item {
            padding: 8px 15px 8px 25px;
            cursor: pointer;
            border-bottom: 1px solid var(--border-color);
            transition: background-color 0.2s;
            font-size: 11px;
        }

        .nav-item:hover {
            background: var(--border-color);
        }

        .nav-item.active {
            background: var(--primary-color);
            color: white;
        }

        .nav-item:last-child {
            border-bottom: none;
        }

        .nav-item-title {
            font-weight: 500;
            margin-bottom: 2px;
        }

        .nav-item-subtitle {
            color: var(--text-secondary);
            font-size: 10px;
        }

        .nav-item.active .nav-item-subtitle {
            color: rgba(255, 255, 255, 0.8);
        }

        .content-area {
            flex: 1;
            padding: 20px;
            overflow-y: auto;
            background: var(--background-color);
        }

        .content-header {
            margin-bottom: 15px;
            padding-bottom: 10px;
            border-bottom: 1px solid var(--border-color);
        }

        .content-title {
            font-size: 16px;
            font-weight: 600;
            color: var(--primary-color);
            margin-bottom: 4px;
        }

        .content-subtitle {
            font-size: 11px;
            color: var(--text-secondary);
        }

        .detail-item {
            margin-bottom: 15px;
            padding: 12px;
            background: var(--surface-color);
            border-radius: 6px;
            border: 1px solid var(--border-color);
        }

        .detail-header {
            font-weight: 600;
            margin-bottom: 6px;
            color: var(--text-color);
            font-size: 12px;
        }

        .detail-path {
            font-size: 10px;
            color: var(--text-secondary);
            margin-bottom: 6px;
            font-family: monospace;
        }

        .detail-content {
            font-size: 11px;
            color: var(--text-secondary);
            line-height: 1.5;
        }

        .issue-item {
            margin: 6px 0;
            padding: 8px;
            background: var(--background-color);
            border-left: 3px solid var(--error-color);
            border-radius: 0 4px 4px 0;
            font-size: 11px;
        }

        .issue-type {
            font-weight: 600;
            color: var(--error-color);
            margin-bottom: 3px;
            font-size: 10px;
        }

        .issue-message {
            color: var(--text-secondary);
            line-height: 1.4;
        }

        .footer {
            text-align: center;
            padding: 10px;
            color: var(--text-secondary);
            border-top: 1px solid var(--border-color);
            font-size: 10px;
        }

        .hidden {
            display: none !important;
        }

        @media (max-width: 768px) {
            .app-layout {
                flex-direction: column;
            }
            
            .sidebar {
                width: 100%;
                height: auto;
                max-height: 40vh;
            }
            
            .filter-box {
                width: 150px;
            }
            
            .stats-bar {
                flex-wrap: wrap;
                gap: 6px;
            }
        }
    </style>
</head>
<body data-theme="light">
    <div class="app-layout">
        <div class="sidebar">
            <div class="sidebar-header">Navigation</div>
            <div class="navigation">
                <div class="nav-section">
                    <div class="nav-section-header" onclick="toggleNavSection('subjects')" id="subjects-header">
                        <span>Subjects</span>
                        <span class="nav-section-count" id="subjects-count">0</span>
                    </div>
                    <div class="nav-section-content" id="subjects-content">
                        <!-- Subjects navigation will be populated by JavaScript -->
                    </div>
                </div>
                
                <div class="nav-section">
                    <div class="nav-section-header" onclick="toggleNavSection('checks')" id="checks-header">
                        <span>Checks</span>
                        <span class="nav-section-count" id="checks-count">0</span>
                    </div>
                    <div class="nav-section-content" id="checks-content">
                        <!-- Checks navigation will be populated by JavaScript -->
                    </div>
                </div>
                
                <div class="nav-section">
                    <div class="nav-section-header" onclick="showAllDetails('pdfs')" id="pdfs-header">
                        <span>PDF Files</span>
                        <span class="nav-section-count" id="pdfs-count">0</span>
                    </div>
                </div>
                
                <div class="nav-section">
                    <div class="nav-section-header" onclick="showAllDetails('skipped')" id="skipped-header">
                        <span>Skipped Files</span>
                        <span class="nav-section-count" id="skipped-count">0</span>
                    </div>
                </div>
                
                <div class="nav-section">
                    <div class="nav-section-header" onclick="showAllDetails('warnings')" id="warnings-header">
                        <span>Warnings</span>
                        <span class="nav-section-count" id="warnings-count">0</span>
                    </div>
                </div>
                
                <div class="nav-section">
                    <div class="nav-section-header" onclick="showAllDetails('errors')" id="errors-header">
                        <span>Errors</span>
                        <span class="nav-section-count" id="errors-count">0</span>
                    </div>
                </div>
            </div>
        </div>

        <div class="main-content">
            <div class="header">
                <h1>{{.Title}}</h1>
                <div class="header-controls">
                    <input type="text" class="filter-box" placeholder="Filter..." id="filterBox">
                    <button class="theme-toggle" onclick="toggleTheme()">🌙 Dark</button>
                </div>
            </div>

            <div class="stats-bar" id="statsBar">
                <!-- Stats will be populated by JavaScript -->
            </div>

            <div class="content-area" id="contentArea">
                <div class="content-header">
                    <div class="content-title" id="contentTitle">Select an item from the navigation</div>
                    <div class="content-subtitle" id="contentSubtitle">Choose a category and item to view details</div>
                </div>
                <div id="contentDetails">
                    <!-- Details will be populated by JavaScript -->
                </div>
            </div>
        </div>
    </div>

    <div class="footer">
        <div class="timestamp">Generated on {{.GeneratedAt}}</div>
    </div>

    <script>
        // Scan data from Go template
        const scanData = {{.JSONData}};
        
        // Global state
        let currentSection = null;
        let currentItem = null;
        
        // Debug: Log the data to console
        console.log('Scan data loaded:', scanData);
        console.log('Scanned files:', scanData.scanned ? scanData.scanned.length : 0);
        console.log('Skipped files:', scanData.skipped ? scanData.skipped.length : 0);
        
        // Theme management
        function toggleTheme() {
            const body = document.body;
            const button = document.querySelector('.theme-toggle');
            const currentTheme = body.getAttribute('data-theme');
            const newTheme = currentTheme === 'light' ? 'dark' : 'light';
            
            body.setAttribute('data-theme', newTheme);
            button.textContent = newTheme === 'light' ? '🌙 Dark' : '☀️ Light';
            
            localStorage.setItem('theme', newTheme);
        }

        // Load saved theme
        document.addEventListener('DOMContentLoaded', function() {
            const savedTheme = localStorage.getItem('theme') || 'light';
            document.body.setAttribute('data-theme', savedTheme);
            const button = document.querySelector('.theme-toggle');
            button.textContent = savedTheme === 'light' ? '🌙 Dark' : '☀️ Light';
        });

        // Navigation section toggle
        function toggleNavSection(sectionName) {
            const header = document.getElementById(sectionName + '-header');
            const content = document.getElementById(sectionName + '-content');
            const isExpanded = content.classList.contains('expanded');
            
            // Close all sections first and clear all active states
            document.querySelectorAll('.nav-section-content').forEach(c => c.classList.remove('expanded'));
            document.querySelectorAll('.nav-section-header').forEach(h => h.classList.remove('active'));
            document.querySelectorAll('.nav-item').forEach(item => item.classList.remove('active'));
            
            if (!isExpanded) {
                // Open this section
                content.classList.add('expanded');
                header.classList.add('active');
                currentSection = sectionName;
                
                // If first time opening, select first item and set focus
                const firstItem = content.querySelector('.nav-item');
                if (firstItem) {
                    selectNavItem(sectionName, firstItem.dataset.id);
                    // Set focus to the content area to enable keyboard navigation
                    content.focus();
                    content.setAttribute('tabindex', '0');
                } else {
                    // No items, but still set focus for potential future items
                    content.focus();
                    content.setAttribute('tabindex', '0');
                }
            } else {
                currentSection = null;
                showEmptyContent();
            }
        }

        // Select navigation item
        function selectNavItem(sectionName, itemId) {
            // Remove active from all nav items and section headers
            document.querySelectorAll('.nav-item').forEach(item => item.classList.remove('active'));
            document.querySelectorAll('.nav-section-header').forEach(h => h.classList.remove('active'));
            
            // Add active to selected item and its section header
            const selectedItem = document.querySelector('[data-section="' + sectionName + '"][data-id="' + itemId + '"]');
            if (selectedItem) {
                selectedItem.classList.add('active');
                document.getElementById(sectionName + '-header').classList.add('active');
                currentItem = itemId;
                showItemDetails(sectionName, itemId);
            }
        }

        // Show item details in main content area
        function showItemDetails(sectionName, itemId) {
            const contentTitle = document.getElementById('contentTitle');
            const contentSubtitle = document.getElementById('contentSubtitle');
            const contentDetails = document.getElementById('contentDetails');
            
            let html = '';
            let title = '';
            let subtitle = '';
            
            switch (sectionName) {
                case 'subjects':
                    const subject = scanData.details_subject_focused ? scanData.details_subject_focused.find(s => s.subject === itemId) : null;
                    if (subject) {
                        title = subject.subject;
                        subtitle = subject.path || 'No path available';
                        html = generateSubjectDetails(subject);
                    }
                    break;
                    
                case 'checks':
                    const check = scanData.details_check_focused ? scanData.details_check_focused.find(c => c.checkname === itemId) : null;
                    if (check) {
                        title = check.checkname;
                        subtitle = (check.issues ? check.issues.length : 0) + ' issues found';
                        html = generateCheckDetails(check);
                    }
                    break;
            }
            
            contentTitle.textContent = title;
            contentSubtitle.textContent = subtitle;
            contentDetails.innerHTML = html;
        }

        // Show empty content
        function showEmptyContent() {
            document.getElementById('contentTitle').textContent = 'Select an item from the navigation';
            document.getElementById('contentSubtitle').textContent = 'Choose a category and item to view details';
            document.getElementById('contentDetails').innerHTML = '';
        }

        // Filter functionality - now filters details instead of navigation
        function filterContent() {
            const filterTerm = document.getElementById('filterBox').value.toLowerCase();
            const detailItems = document.querySelectorAll('.detail-item');
            
            detailItems.forEach(item => {
                const text = item.textContent.toLowerCase();
                if (text.includes(filterTerm) || filterTerm === '') {
                    item.classList.remove('hidden');
                } else {
                    item.classList.add('hidden');
                }
            });
        }

        // Keyboard navigation
        document.addEventListener('keydown', function(event) {
            if (currentSection === 'subjects' || currentSection === 'checks') {
                const content = document.getElementById(currentSection + '-content');
                const items = content.querySelectorAll('.nav-item:not(.hidden)');
                const activeItem = content.querySelector('.nav-item.active');
                
                if (items.length === 0) return;
                
                let currentIndex = -1;
                if (activeItem) {
                    for (let i = 0; i < items.length; i++) {
                        if (items[i] === activeItem) {
                            currentIndex = i;
                            break;
                        }
                    }
                }
                
                let newIndex = currentIndex;
                
                if (event.key === 'ArrowDown') {
                    event.preventDefault();
                    newIndex = Math.min(currentIndex + 1, items.length - 1);
                } else if (event.key === 'ArrowUp') {
                    event.preventDefault();
                    newIndex = Math.max(currentIndex - 1, 0);
                }
                
                if (newIndex !== currentIndex && newIndex >= 0) {
                    const newItem = items[newIndex];
                    selectNavItem(currentSection, newItem.dataset.id);
                    
                    // Scroll the selected item into view
                    newItem.scrollIntoView({
                        behavior: 'smooth',
                        block: 'nearest',
                        inline: 'nearest'
                    });
                }
            }
        });

        // Show all details for simple sections (pdfs, skipped, warnings, errors)
        function showAllDetails(sectionName) {
            // Clear active states from section headers and navigation items
            document.querySelectorAll('.nav-section-header').forEach(h => h.classList.remove('active'));
            document.querySelectorAll('.nav-item').forEach(item => item.classList.remove('active'));
            document.getElementById(sectionName + '-header').classList.add('active');
            
            currentSection = sectionName;
            currentItem = null;
            
            const contentTitle = document.getElementById('contentTitle');
            const contentSubtitle = document.getElementById('contentSubtitle');
            const contentDetails = document.getElementById('contentDetails');
            
            let html = '';
            let title = '';
            let subtitle = '';
            
            switch (sectionName) {
                case 'pdfs':
                    title = 'PDF Files';
                    subtitle = scanData.pdf_files ? scanData.pdf_files.length + ' files' : '0 files';
                    html = generateAllPDFDetails();
                    break;
                    
                case 'skipped':
                    title = 'Skipped Files';
                    subtitle = scanData.skipped ? scanData.skipped.length + ' files' : '0 files';
                    html = generateAllSkippedDetails();
                    break;
                    
                case 'warnings':
                    title = 'Warnings';
                    subtitle = scanData.warnings ? scanData.warnings.length + ' warnings' : '0 warnings';
                    html = generateAllWarningDetails();
                    break;
                    
                case 'errors':
                    title = 'Errors';
                    subtitle = scanData.errors ? scanData.errors.length + ' errors' : '0 errors';
                    html = generateAllErrorDetails();
                    break;
            }
            
            contentTitle.textContent = title;
            contentSubtitle.textContent = subtitle;
            contentDetails.innerHTML = html;
        }

        // Initialize page
        document.addEventListener('DOMContentLoaded', function() {
            populateStats();
            populateNavigation();
            
            // Setup filter
            document.getElementById('filterBox').addEventListener('input', filterContent);
        });

        // Populate statistics
        function populateStats() {
            const stats = [
                { label: 'Scanned', value: scanData.scanned ? scanData.scanned.length : 0, class: 'scanned' },
                { label: 'Issues', value: getTotalIssues(), class: 'issues' },
                { label: 'Skipped', value: scanData.skipped ? scanData.skipped.length : 0, class: 'skipped' },
                { label: 'Warnings', value: scanData.warnings ? scanData.warnings.length : 0, class: 'warnings' },
                { label: 'Errors', value: scanData.errors ? scanData.errors.length : 0, class: 'errors' }
            ];

            const statsBar = document.getElementById('statsBar');
            statsBar.innerHTML = stats.map(stat => 
                '<div class="stat-item">' +
                    '<span class="stat-number ' + stat.class + '">' + stat.value + '</span>' +
                    '<span>' + stat.label + '</span>' +
                '</div>'
            ).join('');
        }

        function getTotalIssues() {
            let total = 0;
            if (scanData.details_subject_focused) {
                scanData.details_subject_focused.forEach(subject => {
                    total += subject.issues ? subject.issues.length : 0;
                });
            }
            return total;
        }

        // Populate navigation
        function populateNavigation() {
            populateSubjectsNav();
            populateChecksNav();
            populatePDFsCount();
            populateSkippedCount();
            populateWarningsCount();
            populateErrorsCount();
        }

        // Populate subjects navigation
        function populateSubjectsNav() {
            const container = document.getElementById('subjects-content');
            const countElement = document.getElementById('subjects-count');
            let html = '';
            
            if (scanData.details_subject_focused && scanData.details_subject_focused.length > 0) {
                countElement.textContent = scanData.details_subject_focused.length;
                
                scanData.details_subject_focused.forEach(subject => {
                    const issueCount = subject.issues ? subject.issues.length : 0;
                    html += '<div class="nav-item" data-section="subjects" data-id="' + escapeHtml(subject.subject) + '" onclick="selectNavItem(\'subjects\', \'' + escapeHtml(subject.subject) + '\')">';
                    html += '<div class="nav-item-title">' + escapeHtml(subject.subject) + '</div>';
                    html += '<div class="nav-item-subtitle">' + issueCount + ' issues</div>';
                    html += '</div>';
                });
            } else {
                countElement.textContent = '0';
                html = '<div class="nav-item">No subjects found</div>';
            }
            
            container.innerHTML = html;
        }

        // Populate checks navigation
        function populateChecksNav() {
            const container = document.getElementById('checks-content');
            const countElement = document.getElementById('checks-count');
            let html = '';
            
            if (scanData.details_check_focused && scanData.details_check_focused.length > 0) {
                countElement.textContent = scanData.details_check_focused.length;
                
                scanData.details_check_focused.forEach(check => {
                    const issueCount = check.issues ? check.issues.length : 0;
                    html += '<div class="nav-item" data-section="checks" data-id="' + escapeHtml(check.checkname) + '" onclick="selectNavItem(\'checks\', \'' + escapeHtml(check.checkname) + '\')">';
                    html += '<div class="nav-item-title">' + escapeHtml(check.checkname) + '</div>';
                    html += '<div class="nav-item-subtitle">' + issueCount + ' issues</div>';
                    html += '</div>';
                });
            } else {
                countElement.textContent = '0';
                html = '<div class="nav-item">No checks found</div>';
            }
            
            container.innerHTML = html;
        }

        // Populate counts only for simple sections
        function populatePDFsCount() {
            const countElement = document.getElementById('pdfs-count');
            countElement.textContent = scanData.pdf_files ? scanData.pdf_files.length : '0';
        }

        function populateSkippedCount() {
            const countElement = document.getElementById('skipped-count');
            countElement.textContent = scanData.skipped ? scanData.skipped.length : '0';
        }

        function populateWarningsCount() {
            const countElement = document.getElementById('warnings-count');
            countElement.textContent = scanData.warnings ? scanData.warnings.length : '0';
        }

        function populateErrorsCount() {
            const countElement = document.getElementById('errors-count');
            countElement.textContent = scanData.errors ? scanData.errors.length : '0';
        }

        // Generate detail content functions
        function generateSubjectDetails(subject) {
            let html = '';
            if (subject.issues && subject.issues.length > 0) {
                subject.issues.forEach(issue => {
                    html += '<div class="detail-item">';
                    html += '<div class="detail-header">' + escapeHtml(issue.checkname) + '</div>';
                    html += '<div class="detail-content">' + escapeHtml(issue.message) + '</div>';
                    html += '</div>';
                });
            } else {
                html = '<div class="detail-item"><div class="detail-content">No issues found for this subject.</div></div>';
            }
            return html;
        }

        function generateCheckDetails(check) {
            let html = '';
            if (check.issues && check.issues.length > 0) {
                check.issues.forEach(issue => {
                    html += '<div class="detail-item">';
                    html += '<div class="detail-header">' + escapeHtml(issue.subject) + '</div>';
                    if (issue.path) {
                        html += '<div class="detail-path">' + escapeHtml(issue.path) + '</div>';
                    }
                    html += '<div class="detail-content">' + escapeHtml(issue.message) + '</div>';
                    html += '</div>';
                });
            } else {
                html = '<div class="detail-item"><div class="detail-content">No issues found for this check.</div></div>';
            }
            return html;
        }

        function generateSkippedDetails(skipped) {
            let html = '<div class="detail-item">';
            html += '<div class="detail-header">File Details</div>';
            if (skipped.path) {
                html += '<div class="detail-path">' + escapeHtml(skipped.path) + '</div>';
            }
            html += '<div class="detail-content"><strong>Reason:</strong> ' + escapeHtml(skipped.reason) + '</div>';
            html += '</div>';
            return html;
        }

        function generateWarningDetails(warning) {
            let html = '<div class="detail-item">';
            html += '<div class="detail-header">Warning Details</div>';
            html += '<div class="detail-content">' + escapeHtml(warning.message) + '</div>';
            html += '</div>';
            return html;
        }

        function generateErrorDetails(error) {
            let html = '<div class="detail-item">';
            html += '<div class="detail-header">Error Details</div>';
            html += '<div class="detail-content">' + escapeHtml(error.message) + '</div>';
            html += '</div>';
            return html;
        }

        // Generate all details functions for simple sections
        function generateAllPDFDetails() {
            let html = '';
            if (scanData.pdf_files && scanData.pdf_files.length > 0) {
                scanData.pdf_files.forEach((file, index) => {
                    html += '<div class="detail-item">';
                    html += '<div class="detail-header">PDF File ' + (index + 1) + '</div>';
                    html += '<div class="detail-content">' + escapeHtml(file) + '</div>';
                    html += '</div>';
                });
            } else {
                html = '<div class="detail-item"><div class="detail-content">No PDF files found.</div></div>';
            }
            return html;
        }

        function generateAllSkippedDetails() {
            let html = '';
            if (scanData.skipped && scanData.skipped.length > 0) {
                scanData.skipped.forEach(file => {
                    html += '<div class="detail-item">';
                    html += '<div class="detail-header">' + escapeHtml(file.filename) + '</div>';
                    if (file.path) {
                        html += '<div class="detail-path">' + escapeHtml(file.path) + '</div>';
                    }
                    html += '<div class="detail-content"><strong>Reason:</strong> ' + escapeHtml(file.reason) + '</div>';
                    html += '</div>';
                });
            } else {
                html = '<div class="detail-item"><div class="detail-content">No skipped files found.</div></div>';
            }
            return html;
        }

        function generateAllWarningDetails() {
            let html = '';
            if (scanData.warnings && scanData.warnings.length > 0) {
                scanData.warnings.forEach((warning, index) => {
                    html += '<div class="detail-item">';
                    html += '<div class="detail-header">Warning ' + (index + 1) + '</div>';
                    html += '<div class="detail-path">' + escapeHtml(warning.timestamp) + '</div>';
                    html += '<div class="detail-content">' + escapeHtml(warning.message) + '</div>';
                    html += '</div>';
                });
            } else {
                html = '<div class="detail-item"><div class="detail-content">No warnings found.</div></div>';
            }
            return html;
        }

        function generateAllErrorDetails() {
            let html = '';
            if (scanData.errors && scanData.errors.length > 0) {
                scanData.errors.forEach((error, index) => {
                    html += '<div class="detail-item">';
                    html += '<div class="detail-header">Error ' + (index + 1) + '</div>';
                    html += '<div class="detail-path">' + escapeHtml(error.timestamp) + '</div>';
                    html += '<div class="detail-content">' + escapeHtml(error.message) + '</div>';
                    html += '</div>';
                });
            } else {
                html = '<div class="detail-item"><div class="detail-content">No errors found.</div></div>';
            }
            return html;
        }

        // Utility function to escape HTML
        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }
    </script>
</body>
</html>`