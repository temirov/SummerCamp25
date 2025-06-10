// cmd/scrape/main.go
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/chromedp"
)

const (
	chromeExecutablePath     = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	scrapeTimeout            = 45 * time.Second
	postClickSleepDuration   = 2 * time.Second
	postScrapeSleepDuration  = 500 * time.Millisecond
	subActivitiesSelector    = `//a[contains(normalize-space(.),"View sub-activities")]`
	activityCardSelector     = `.activity-card`
	subActivitiesLinkText    = "View sub-activities"
	titleSelector            = `.activity-card-info__name`
	dateRangeSelector        = `.activity-card-info__dateRange > span`
	timeRangeSelector        = `.activity-card-info__timeRange > span`
	ageSelector              = `.activity-card-info__ages`
	cornerMarkSelector       = `.activity-card__cornerMark`
	alertTextSelector        = `.activity-card-alert__text`
	bodySelector             = `body`
	ageRegexPattern          = `at least (\d+) yrs but less than (\d+) yrs`
	defaultAvailability      = "Available"
	flagCSVParameterName     = "csv"
	flagCSVParameterUsage    = "path to CSV file with a Camp column"
	flagOutputParameterName  = "out"
	flagOutputParameterUsage = "path to file for the combined JSON output"
	baseSearchURL            = "https://anc.apm.activecommunities.com/citymb/activity/search?onlineSiteId=0&activity_select_param=2&viewMode=list&activity_keyword=%s"
)

var ageRegex = regexp.MustCompile(ageRegexPattern)
var weekdayIndex = map[string]int{"Mon": 0, "Tue": 1, "Wed": 2, "Thu": 3, "Fri": 4, "Sat": 5, "Sun": 6}

type Session struct {
	Title         string   `json:"title"`
	StartDateUnix int64    `json:"startDateUnix"`
	EndDateUnix   int64    `json:"endDateUnix"`
	Days          []string `json:"days"`
	StartMinutes  int      `json:"startMinutes"`
	EndMinutes    int      `json:"endMinutes"`
	MinAge        *int     `json:"minAge,omitempty"`
	MaxAge        *int     `json:"maxAge,omitempty"`
	Availability  string   `json:"availability"`
	PageURL       string   `json:"pageUrl"`
}

func main() {
	csvFilePath := flag.String(flagCSVParameterName, "", flagCSVParameterUsage)
	outputFilePath := flag.String(flagOutputParameterName, "", flagOutputParameterUsage)
	flag.Parse()
	if *csvFilePath == "" {
		log.Fatalf("FATAL: -%s is required", flagCSVParameterName)
	}
	if *outputFilePath == "" {
		log.Fatalf("FATAL: -%s is required", flagOutputParameterName)
	}
	campNames, err := loadCampNames(*csvFilePath)
	if err != nil {
		log.Fatalf("FATAL: loading CSV %q: %v", *csvFilePath, err)
	}
	if len(campNames) == 0 {
		log.Fatalf("FATAL: no camp names found in %s", *csvFilePath)
	}
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutablePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...,
	)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()
	if err := chromedp.Run(browserCtx); err != nil {
		log.Fatalf("FATAL: starting Chrome: %v", err)
	}
	var combined []Session
	for _, campName := range campNames {
		navigateURL := fmt.Sprintf(baseSearchURL, url.QueryEscape(campName))
		log.Printf("Scraping %q → %s", campName, navigateURL)
		items, err := scrapePage(browserCtx, navigateURL)
		if err != nil {
			log.Printf("  → ERROR scraping %q: %v", campName, err)
			continue
		}
		combined = append(combined, items...)
		time.Sleep(postScrapeSleepDuration)
	}
	outFile, err := os.Create(*outputFilePath)
	if err != nil {
		log.Fatalf("FATAL: creating %s: %v", *outputFilePath, err)
	}
	defer outFile.Close()
	enc := json.NewEncoder(outFile)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(combined); err != nil {
		log.Fatalf("FATAL: writing JSON: %v", err)
	}
	log.Printf("Done: wrote %d sessions to %s", len(combined), *outputFilePath)
}

func loadCampNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, n := range header {
		if n == "Camp" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("no 'Camp' column in header")
	}
	var names []string
	for {
		row, err := r.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if idx < len(row) {
			if v := strings.TrimSpace(row[idx]); v != "" {
				names = append(names, v)
			}
		}
	}
	return names, nil
}

func mustParse(layout, value string) time.Time {
	t, err := time.Parse(layout, value)
	if err != nil {
		panic(value)
	}
	return t
}

func clockMinutes(raw string) int {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return 0
	}
	if s == "NOON" {
		return 12 * 60
	}
	layouts := []string{"3:04 PM", "3PM", "3 PM", "15:04", "15"}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Hour()*60 + t.Minute()
		}
	}
	panic(raw)
}

func expandDays(token string) []string {
	var out []string
	for _, seg := range strings.Split(token, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if strings.Contains(seg, "-") {
			parts := strings.Split(seg, "-")
			a := strings.TrimSpace(parts[0])
			b := strings.TrimSpace(parts[1])
			si, ok1 := weekdayIndex[a]
			ei, ok2 := weekdayIndex[b]
			if !ok1 || !ok2 {
				continue
			}
			for i := 0; ; i++ {
				idx := (si + i) % 7
				for k, v := range weekdayIndex {
					if v == idx {
						out = append(out, k)
						break
					}
				}
				if idx == ei {
					break
				}
			}
		} else {
			out = append(out, seg)
		}
	}
	return out
}

func parseDateRange(r string) (time.Time, time.Time) {
	if strings.Contains(r, "to") {
		parts := strings.Split(r, "to")
		return mustParse("January 2, 2006", strings.TrimSpace(parts[0])), mustParse("January 2, 2006", strings.TrimSpace(parts[1]))
	}
	t := mustParse("January 2, 2006", strings.TrimSpace(r))
	return t, t
}

func splitTimeRange(inp string) (int, int, []string) {
	pos := strings.Index(inp, " ")
	if pos == -1 {
		return 0, 0, nil
	}
	dayTok := strings.TrimSpace(inp[:pos])
	timeSeg := strings.TrimSpace(inp[pos+1:])
	parts := strings.Split(timeSeg, "-")
	if len(parts) != 2 {
		return 0, 0, nil
	}
	a := strings.TrimSpace(parts[0])
	b := strings.TrimSpace(parts[1])
	hasMer := func(s string) bool {
		u := strings.ToUpper(s)
		return strings.Contains(u, "AM") || strings.Contains(u, "PM") || u == "NOON"
	}
	switch {
	case hasMer(a) && !hasMer(b):
		if strings.EqualFold(a, "Noon") {
			b += " PM"
		} else if strings.Contains(strings.ToUpper(a), "AM") {
			b += " AM"
		} else {
			b += " PM"
		}
	case !hasMer(a) && hasMer(b):
		if strings.EqualFold(b, "Noon") {
			a += " AM"
		} else if strings.Contains(strings.ToUpper(b), "AM") {
			a += " AM"
		} else {
			a += " PM"
		}
	}
	start := clockMinutes(a)
	end := clockMinutes(b)
	return start, end, expandDays(dayTok)
}

func scrapePage(parent context.Context, pageURL string) ([]Session, error) {
	ctx, cancel := context.WithTimeout(parent, scrapeTimeout)
	defer cancel()
	var html string
	err := chromedp.Run(ctx,
		chromedp.Navigate(pageURL),
		chromedp.WaitVisible(activityCardSelector, chromedp.ByQuery),
		chromedp.Sleep(postClickSleepDuration),
		chromedp.ActionFunc(func(a context.Context) error {
			_ = chromedp.Click(subActivitiesSelector, chromedp.BySearch, chromedp.AtLeast(0)).Do(a)
			return nil
		}),
		chromedp.Sleep(postClickSleepDuration),
		chromedp.OuterHTML(bodySelector, &html, chromedp.ByQuery),
	)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	var list []Session
	doc.Find(activityCardSelector).Each(func(i int, s *goquery.Selection) {
		if s.Find("a").FilterFunction(func(_ int, q *goquery.Selection) bool { return strings.Contains(q.Text(), subActivitiesLinkText) }).Length() > 0 {
			return
		}
		title := strings.TrimSpace(s.Find(titleSelector).Text())
		dateText := strings.TrimSpace(s.Find(dateRangeSelector).Text())
		timeText := strings.TrimSpace(s.Find(timeRangeSelector).Text())
		ageText := strings.TrimSpace(s.Find(ageSelector).Text())
		var minPtr, maxPtr *int
		if m := ageRegex.FindStringSubmatch(ageText); len(m) == 3 {
			if v, err := strconv.Atoi(m[1]); err == nil {
				minPtr = &v
			}
			if v, err := strconv.Atoi(m[2]); err == nil {
				maxPtr = &v
			}
		}
		availability := defaultAvailability
		if csel := s.Find(cornerMarkSelector); csel.Length() > 0 {
			availability = strings.TrimSpace(csel.Text())
		} else if asel := s.Find(alertTextSelector); asel.Length() > 0 {
			availability = strings.TrimSpace(asel.Text())
		}
		ds, de := parseDateRange(dateText)
		startM, endM, days := splitTimeRange(timeText)
		list = append(list, Session{
			Title:         title,
			StartDateUnix: ds.Unix(),
			EndDateUnix:   de.Unix(),
			Days:          days,
			StartMinutes:  startM,
			EndMinutes:    endM,
			MinAge:        minPtr,
			MaxAge:        maxPtr,
			Availability:  availability,
			PageURL:       pageURL,
		})
	})
	return list, nil
}
