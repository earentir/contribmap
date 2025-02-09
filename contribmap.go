package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	cli "github.com/jawher/mow.cli"
)

// =============================================================================
// Shared Constants and Color Schemes
// =============================================================================

// Define the GitHub GraphQL API endpoint.
const githubGraphQLEndpoint = "https://api.github.com/graphql"

const (
	// Background colors for the contribution map (which follows lightMode)
	bgDark  = "#000000"
	bgLight = "#ffffff"

	// Number of nonzero color buckets for the map
	bucketCount = 5

	// Dark mode bucket colors (from darkest to brightest)
	darkBucketColors0 = "#0B3D0B" // bucket 1 (lowest nonzero)
	darkBucketColors1 = "#0F4F0F" // bucket 2
	darkBucketColors2 = "#129012" // bucket 3 (mid level)
	darkBucketColors3 = "#16B316" // bucket 4
	darkBucketColors4 = "#1AFF1A" // bucket 5 (brightest)

	// Light mode bucket colors (for a light background)
	lightBucketColors0 = "#216e39"
	lightBucketColors1 = "#30a14e"
	lightBucketColors2 = "#40c463"
	lightBucketColors3 = "#8fdc85"
	lightBucketColors4 = "#c6f7d0"

	// Colors for days with zero contributions
	zeroColorDark  = "#000000"
	zeroColorLight = "#ebedf0"
)

// Arrays to group bucket colors.
var (
	darkBucketColors  = [bucketCount]string{darkBucketColors0, darkBucketColors1, darkBucketColors2, darkBucketColors3, darkBucketColors4}
	lightBucketColors = [bucketCount]string{lightBucketColors0, lightBucketColors1, lightBucketColors2, lightBucketColors3, lightBucketColors4}
)

// =============================================================================
// Other Layout Constants
// =============================================================================

const (
	// Map layout
	cellSize   = 12
	cellMargin = 2
	topMargin  = 20 // extra vertical space at the top for month labels

	// Cross diagram dimensions and arm coordinates
	crossSVGWidth  = 300
	crossSVGHeight = 300
	crossCenterX   = crossSVGWidth / 2
	crossCenterY   = crossSVGHeight / 2

	// Where to place the labels along the arms:
	topY    = 50  // for Code Reviews (top)
	bottomY = 250 // for Pull Requests (bottom)
	leftX   = 50  // for Commits (left)
	rightX  = 250 // for Issues (right)
)

// =============================================================================
// Data Structures
// =============================================================================

// --- GitHub GraphQL API Types ---
type GitHubContributionDay struct {
	Date              string `json:"date"`
	ContributionCount int    `json:"contributionCount"`
}

type GitHubWeek struct {
	ContributionDays []GitHubContributionDay `json:"contributionDays"`
}

type GitHubContributionCalendar struct {
	TotalContributions int          `json:"totalContributions"`
	Weeks              []GitHubWeek `json:"weeks"`
}

type GitHubContributionsCollection struct {
	ContributionCalendar                GitHubContributionCalendar `json:"contributionCalendar"`
	TotalCommitContributions            int                        `json:"totalCommitContributions"`
	TotalPullRequestContributions       int                        `json:"totalPullRequestContributions"`
	TotalIssueContributions             int                        `json:"totalIssueContributions"`
	TotalPullRequestReviewContributions int                        `json:"totalPullRequestReviewContributions"`
}

type GitHubUser struct {
	ContributionsCollection GitHubContributionsCollection `json:"contributionsCollection"`
}

type GitHubResponseData struct {
	User GitHubUser `json:"user"`
}

type GitHubGraphQLResponse struct {
	Data GitHubResponseData `json:"data"`
}

// --- Our Generic Types ---
type ContributionDay struct {
	Date  string
	Count int
	Color string
}

// Weeks is a slice of weeks; each week is a slice of 7 ContributionDay values.
type Weeks [][]ContributionDay

// MonthLabel holds an x coordinate and the label (three‑letter month).
type MonthLabel struct {
	X     int
	Label string
}

// CrossData holds the totals for the four contribution types.
type CrossData struct {
	Commits      int
	PullRequests int
	Issues       int
	CodeReviews  int
}

// --- Gitea Event Type ---
// For Gitea we expect the events API to return at least these fields.
type GiteaEvent struct {
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
}

// =============================================================================
// Color Functions for the Map
// =============================================================================

// getColor returns a hex color string for a given day's contribution count.
// It splits the range 1..maxCount equally into bucketCount buckets. The lowest
// bucket gets the darkest green and the highest gets the lightest green.
func getColor(count int, maxCount int, lightMode bool) string {
	if count == 0 {
		if lightMode {
			return zeroColorLight
		}
		return zeroColorDark
	}
	// Compute bucket width (ensuring at least 1)
	bucketWidth := int(math.Ceil(float64(maxCount-1) / float64(bucketCount)))
	if bucketWidth < 1 {
		bucketWidth = 1
	}
	bucketIndex := (count - 1) / bucketWidth
	if bucketIndex >= bucketCount {
		bucketIndex = bucketCount - 1
	}
	if lightMode {
		return lightBucketColors[bucketIndex]
	}
	return darkBucketColors[bucketIndex]
}

// =============================================================================
// Data Fetching Functions
// =============================================================================

// fetchGitHubContributions queries GitHub’s GraphQL API for both the daily
// contributions (for the map) and the breakdown totals (for the cross diagram).
func fetchGitHubContributions(username, token string, lightMode bool) (Weeks, CrossData, error) {
	query := `
	query($login: String!) {
	  user(login: $login) {
	    contributionsCollection {
	      totalCommitContributions
	      totalPullRequestContributions
	      totalIssueContributions
	      totalPullRequestReviewContributions
	      contributionCalendar {
	        totalContributions
	        weeks {
	          contributionDays {
	            date
	            contributionCount
	          }
	        }
	      }
	    }
	  }
	}`
	variables := map[string]interface{}{
		"login": username,
	}
	reqBody := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, CrossData{}, err
	}

	req, err := http.NewRequest("POST", githubGraphQLEndpoint, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return nil, CrossData{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, CrossData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return nil, CrossData{}, fmt.Errorf("GitHub API error: %s", string(bodyBytes))
	}

	var gqlResp GitHubGraphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, CrossData{}, err
	}

	var weeks Weeks
	for _, week := range gqlResp.Data.User.ContributionsCollection.ContributionCalendar.Weeks {
		var days []ContributionDay
		for _, day := range week.ContributionDays {
			// Leave Color empty for now; update after computing max.
			days = append(days, ContributionDay{
				Date:  day.Date,
				Count: day.ContributionCount,
				Color: "",
			})
		}
		weeks = append(weeks, days)
	}

	cc := gqlResp.Data.User.ContributionsCollection
	crossData := CrossData{
		Commits:      cc.TotalCommitContributions,
		PullRequests: cc.TotalPullRequestContributions,
		Issues:       cc.TotalIssueContributions,
		CodeReviews:  cc.TotalPullRequestReviewContributions,
	}

	return weeks, crossData, nil
}

// fetchGiteaContributions queries Gitea’s events API for the given user,
// aggregates daily totals (for the map) and also computes a breakdown (for the cross diagram).
func fetchGiteaContributions(username, baseURL string, lightMode bool) (Weeks, CrossData, error) {
	url := fmt.Sprintf("%s/api/v1/users/%s/events", baseURL, username)
	resp, err := http.Get(url)
	if err != nil {
		return nil, CrossData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return nil, CrossData{}, fmt.Errorf("Gitea API error: %s", string(bodyBytes))
	}

	var events []GiteaEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, CrossData{}, err
	}

	contributionsMap := make(map[string]int)
	var crossData CrossData

	// Classify events (adjust these mappings as needed)
	for _, event := range events {
		eventType := strings.ToLower(event.Type)
		t, err := time.Parse(time.RFC3339, event.CreatedAt)
		if err != nil {
			continue
		}
		dateStr := t.Format("2006-01-02")
		contributionsMap[dateStr]++

		switch eventType {
		case "pushevent":
			crossData.Commits++
		case "pullrequestevent":
			crossData.PullRequests++
		case "issuestatechangeevent", "issueevent":
			crossData.Issues++
		case "pullrequestcommentevent", "pullrequestreviewevent":
			crossData.CodeReviews++
		}
	}

	// Build the Weeks grid covering roughly the past year.
	today := time.Now()
	startDate := today.AddDate(0, 0, -364)
	weekday := startDate.Weekday()
	startDate = startDate.AddDate(0, 0, -int(weekday))

	var weeks Weeks
	var currentWeek []ContributionDay
	currentDate := startDate
	for !currentDate.After(today) {
		dateStr := currentDate.Format("2006-01-02")
		count := contributionsMap[dateStr]
		currentWeek = append(currentWeek, ContributionDay{
			Date:  dateStr,
			Count: count,
			Color: "",
		})
		if currentDate.Weekday() == time.Saturday {
			weeks = append(weeks, currentWeek)
			currentWeek = []ContributionDay{}
		}
		currentDate = currentDate.AddDate(0, 0, 1)
	}
	if len(currentWeek) > 0 {
		for len(currentWeek) < 7 {
			currentWeek = append(currentWeek, ContributionDay{
				Date:  "",
				Count: 0,
				Color: "",
			})
		}
		weeks = append(weeks, currentWeek)
	}

	return weeks, crossData, nil
}

// =============================================================================
// Post-Processing: Update Colors for the Map
// =============================================================================

// updateWeeksColors computes the maximum daily count and then updates every day's Color.
func updateWeeksColors(weeks Weeks, lightMode bool) {
	maxCount := 0
	for _, week := range weeks {
		for _, day := range week {
			if day.Count > maxCount {
				maxCount = day.Count
			}
		}
	}
	for i, week := range weeks {
		for j, day := range week {
			weeks[i][j].Color = getColor(day.Count, maxCount, lightMode)
		}
	}
}

// =============================================================================
// SVG Generation Functions
// =============================================================================

// generateSVG produces the contribution map as an SVG file.
// The map obeys the light/dark mode selection.
func generateSVG(weeks Weeks, outputFilename string, lightMode bool) error {
	numWeeks := len(weeks)
	gridWidth := numWeeks*(cellSize+cellMargin) + cellMargin
	gridHeight := 7*(cellSize+cellMargin) + cellMargin
	svgWidth := gridWidth
	svgHeight := topMargin + gridHeight

	var svg bytes.Buffer
	svg.WriteString(fmt.Sprintf(`<svg width="%d" height="%d" xmlns="http://www.w3.org/2000/svg">`, svgWidth, svgHeight))
	svg.WriteString("\n")
	if lightMode {
		svg.WriteString(fmt.Sprintf(`<rect width="%d" height="%d" fill="%s"/>`, svgWidth, svgHeight, bgLight))
	} else {
		svg.WriteString(fmt.Sprintf(`<rect width="%d" height="%d" fill="%s"/>`, svgWidth, svgHeight, bgDark))
	}
	svg.WriteString("\n")

	// Determine month labels (three-letter abbreviation when a month begins).
	var monthLabels []MonthLabel
	for weekIndex, week := range weeks {
		for _, day := range week {
			if day.Date != "" {
				t, err := time.Parse("2006-01-02", day.Date)
				if err != nil {
					continue
				}
				if t.Day() == 1 {
					label := t.Format("Jan")
					if len(monthLabels) == 0 || monthLabels[len(monthLabels)-1].Label != label {
						x := cellMargin + weekIndex*(cellSize+cellMargin)
						monthLabels = append(monthLabels, MonthLabel{X: x, Label: label})
					}
					break
				}
			}
		}
	}

	// Text color follows the mode.
	textFill := "black"
	if !lightMode {
		textFill = "white"
	}
	for _, ml := range monthLabels {
		svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" fill="%s" font-family="sans-serif" font-size="10px">%s</text>`, ml.X, topMargin-4, textFill, ml.Label))
		svg.WriteString("\n")
	}

	// Draw each cell.
	for weekIndex, week := range weeks {
		for dayIndex, day := range week {
			x := cellMargin + weekIndex*(cellSize+cellMargin)
			y := topMargin + cellMargin + dayIndex*(cellSize+cellMargin)
			strokeAttr := ""
			if !lightMode {
				strokeAttr = ` stroke="#333333" stroke-width="1"`
			}
			tooltip := ""
			if day.Date != "" {
				tooltip = fmt.Sprintf("%s: %d contributions", day.Date, day.Count)
			}
			rect := fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" fill="%s"%s>
  <title>%s</title>
</rect>`, x, y, cellSize, cellSize, day.Color, strokeAttr, tooltip)
			svg.WriteString(rect)
			svg.WriteString("\n")
		}
	}

	svg.WriteString("</svg>")
	return ioutil.WriteFile(outputFilename, svg.Bytes(), 0644)
}

// generateCrossSVG produces an SVG “cross” diagram showing the breakdown of four contribution types.
// The layout is as follows:
//   - Top: Code Reviews
//   - Bottom: Pull Requests
//   - Left: Commits
//   - Right: Issues
//
// In addition to printing the label and percentage at each arm, this function computes a weighted (x, y)
// point and draws a large circle (dot) at that point. This function now obeys the lightMode flag:
// if lightMode is true, the cross diagram uses a white background, and the dot and text are chosen
// from the light color scheme; otherwise, it uses a black background with the dark scheme.
func generateCrossSVG(crossData CrossData, outputFilename string, lightMode bool) error {
	total := crossData.Commits + crossData.PullRequests + crossData.Issues + crossData.CodeReviews
	var commitsPerc, prPerc, issuesPerc, codeReviewsPerc float64
	if total > 0 {
		commitsPerc = float64(crossData.Commits) / float64(total) * 100
		prPerc = float64(crossData.PullRequests) / float64(total) * 100
		issuesPerc = float64(crossData.Issues) / float64(total) * 100
		codeReviewsPerc = float64(crossData.CodeReviews) / float64(total) * 100
	}

	// Choose colors based on the lightMode flag.
	var bg, dot, text string
	if lightMode {
		bg = bgLight
		dot = lightBucketColors[4]  // brightest green from light scheme
		text = lightBucketColors[2] // mid-level green from light scheme
	} else {
		bg = bgDark
		dot = darkBucketColors[4]  // brightest green from dark scheme
		text = darkBucketColors[2] // mid-level green from dark scheme
	}

	var svg bytes.Buffer
	svg.WriteString(fmt.Sprintf(`<svg width="%d" height="%d" xmlns="http://www.w3.org/2000/svg">`, crossSVGWidth, crossSVGHeight))
	svg.WriteString("\n")
	// Background
	svg.WriteString(fmt.Sprintf(`<rect width="%d" height="%d" fill="%s"/>`, crossSVGWidth, crossSVGHeight, bg))
	svg.WriteString("\n")
	// Draw dashed cross lines using the dot color.
	svg.WriteString(fmt.Sprintf(`<line x1="%d" y1="0" x2="%d" y2="%d" stroke="%s" stroke-dasharray="4"/>`, crossCenterX, crossCenterX, crossSVGHeight, dot))
	svg.WriteString("\n")
	svg.WriteString(fmt.Sprintf(`<line x1="0" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-dasharray="4"/>`, crossCenterY, crossSVGWidth, crossCenterY, dot))
	svg.WriteString("\n")
	// Top: Code Reviews
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="14px" fill="%s">Code Reviews</text>`, crossCenterX, topY, text))
	svg.WriteString("\n")
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="12px" fill="%s">%0.1f%%</text>`, crossCenterX, topY+18, text, codeReviewsPerc))
	svg.WriteString("\n")
	// Bottom: Pull Requests
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="14px" fill="%s">Pull Requests</text>`, crossCenterX, bottomY, text))
	svg.WriteString("\n")
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="12px" fill="%s">%0.1f%%</text>`, crossCenterX, bottomY+18, text, prPerc))
	svg.WriteString("\n")
	// Left: Commits
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="14px" fill="%s">Commits</text>`, leftX, crossCenterY, text))
	svg.WriteString("\n")
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="12px" fill="%s">%0.1f%%</text>`, leftX, crossCenterY+18, text, commitsPerc))
	svg.WriteString("\n")
	// Right: Issues
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="14px" fill="%s">Issues</text>`, rightX, crossCenterY, text))
	svg.WriteString("\n")
	svg.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" font-family="sans-serif" font-size="12px" fill="%s">%0.1f%%</text>`, rightX, crossCenterY+18, text, issuesPerc))
	svg.WriteString("\n")

	// Compute the weighted (x, y) point.
	var x, y float64
	if (crossData.Commits + crossData.Issues) > 0 {
		// x coordinate: interpolate from left (commits) to right (issues)
		x = float64(leftX) + (float64(crossData.Issues)/float64(crossData.Commits+crossData.Issues))*float64(rightX-leftX)
	} else {
		x = float64(crossCenterX)
	}
	if (crossData.CodeReviews + crossData.PullRequests) > 0 {
		// y coordinate: interpolate from top (code reviews) to bottom (pull requests)
		y = float64(topY) + (float64(crossData.PullRequests)/float64(crossData.CodeReviews+crossData.PullRequests))*float64(bottomY-topY)
	} else {
		y = float64(crossCenterY)
	}
	// Draw a big circle (dot) at the computed point.
	svg.WriteString(fmt.Sprintf(`<circle cx="%0.1f" cy="%0.1f" r="10" fill="%s"/>`, x, y, dot))
	svg.WriteString("\n")

	svg.WriteString("</svg>")
	return ioutil.WriteFile(outputFilename, svg.Bytes(), 0644)
}

// =============================================================================
// Main (using mow.cli)
// =============================================================================

func main() {
	app := cli.App("contribmap", "Generate a contribution map (heatmap) and a cross diagram showing contribution breakdowns for GitHub or Gitea users.")

	platform := app.String(cli.StringOpt{
		Name:  "platform",
		Value: "github",
		Desc:  "Platform to use: github or gitea",
	})
	user := app.String(cli.StringOpt{
		Name: "user",
		Desc: "Username on the chosen platform",
	})
	token := app.String(cli.StringOpt{
		Name: "token",
		Desc: "GitHub token (required for GitHub; not needed for Gitea)",
	})
	giteaURL := app.String(cli.StringOpt{
		Name:  "gitea-url",
		Value: "https://try.gitea.io",
		Desc:  "Base URL for Gitea instance (used if platform is gitea)",
	})
	lightMode := app.Bool(cli.BoolOpt{
		Name:  "light-mode",
		Value: false,
		Desc:  "Use the light color scheme for both the map and cross diagram (default is dark mode)",
	})
	outputFormat := app.String(cli.StringOpt{
		Name:  "output",
		Value: "svg",
		Desc:  "Output format (default 'svg')",
	})

	app.Action = func() {
		if *user == "" {
			fmt.Println("Please provide a username using the --user option.")
			os.Exit(1)
		}
		if *outputFormat != "svg" {
			fmt.Fprintf(os.Stderr, "Unknown output format: %s. Currently only 'svg' is supported.\n", *outputFormat)
			os.Exit(1)
		}

		var weeks Weeks
		var crossData CrossData
		var err error

		if strings.ToLower(*platform) == "github" {
			if *token == "" {
				fmt.Println("A GitHub token is required when using the GitHub platform. Provide it using the --token option.")
				os.Exit(1)
			}
			fmt.Printf("Fetching contributions for GitHub user %s...\n", *user)
			weeks, crossData, err = fetchGitHubContributions(*user, *token, *lightMode)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching GitHub contributions: %v\n", err)
				os.Exit(1)
			}
		} else if strings.ToLower(*platform) == "gitea" {
			fmt.Printf("Fetching contributions for Gitea user %s from %s...\n", *user, *giteaURL)
			weeks, crossData, err = fetchGiteaContributions(*user, *giteaURL, *lightMode)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching Gitea contributions: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Unknown platform: %s. Use 'github' or 'gitea'.\n", *platform)
			os.Exit(1)
		}

		updateWeeksColors(weeks, *lightMode)
		mapFilename := "contributions.svg"
		if err := generateSVG(weeks, mapFilename, *lightMode); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating contribution map: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Contribution map generated and saved to %s\n", mapFilename)

		crossFilename := "contributions_cross.svg"
		if err := generateCrossSVG(crossData, crossFilename, *lightMode); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating cross diagram: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Cross diagram generated and saved to %s\n", crossFilename)
	}

	app.Run(os.Args)
}
