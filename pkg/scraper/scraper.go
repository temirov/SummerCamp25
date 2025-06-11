// pkg/scraper/scraper.go
package scraper

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"SummerCamp25/pkg/log"
	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

var chromeExecutablePath = func() string {
	if path, _ := exec.LookPath("google-chrome"); path != "" {
		return path
	}
	if path, _ := exec.LookPath("chromium"); path != "" {
		return path
	}
	return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
}()

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

const (
	searchURLTemplate          = "https://anc.apm.activecommunities.com/citymb/activity/search?onlineSiteId=0&activity_select_param=2&viewMode=list&activity_keyword=%s"
	activityCardSelector       = ".activity-card"
	activityTitleSelector      = ".activity-card-info__name"
	dateRangeSelector          = ".activity-card-info__dateRange > span"
	timeRangeSelector          = ".activity-card-info__timeRange > span"
	ageSelector                = ".activity-card-info__ages"
	cornerMarkSelector         = ".activity-card__cornerMark"
	alertTextSelector          = ".activity-card-alert__text"
	subActivitiesAnchorXPath   = `//a[contains(normalize-space(.),"View sub-activities")]`
	defaultAvailabilityLiteral = "Available"
)

var (
	ageRegularExpression = regexp.MustCompile(`at least (\d+) yrs but less than (\d+) yrs`)
	weekdayToIndexMap    = map[string]int{"Mon": 0, "Tue": 1, "Wed": 2, "Thu": 3, "Fri": 4, "Sat": 5, "Sun": 6}
)

func Scrape(parentContext context.Context, keywordList []string) ([]Session, error) {
	if len(keywordList) == 0 {
		return nil, errors.New("keywords slice is empty")
	}
	log.L().Info("scrape_start", zap.Int("keywords", len(keywordList)))

	allocatorContext, allocatorCancel := chromedp.NewExecAllocator(
		parentContext,
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutablePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...,
	)
	defer allocatorCancel()

	browserContext, browserCancel := chromedp.NewContext(allocatorContext)
	defer browserCancel()

	if err := chromedp.Run(browserContext); err != nil {
		return nil, err
	}

	var combinedSessions []Session
	for _, keyword := range keywordList {
		searchURL := fmt.Sprintf(searchURLTemplate, url.QueryEscape(keyword))
		log.L().Info("scrape_keyword", zap.String("keyword", keyword), zap.String("url", searchURL))
		keywordSessions, scrapeError := scrapeSingleKeyword(browserContext, searchURL)
		if scrapeError != nil {
			log.L().Warn("scrape_error", zap.String("keyword", keyword), zap.Error(scrapeError))
			continue
		}
		combinedSessions = append(combinedSessions, keywordSessions...)
	}
	log.L().Info("scrape_done", zap.Int("sessions", len(combinedSessions)))
	return combinedSessions, nil
}

func scrapeSingleKeyword(parentContext context.Context, pageURL string) ([]Session, error) {
	contextWithTimeout, contextCancel := context.WithTimeout(parentContext, 60*time.Second)
	defer contextCancel()

	var pageHTML string
	runError := chromedp.Run(
		contextWithTimeout,
		chromedp.Navigate(pageURL),
		chromedp.WaitVisible(activityCardSelector, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_ = chromedp.Click(subActivitiesAnchorXPath, chromedp.BySearch, chromedp.AtLeast(0)).Do(ctx)
			return nil
		}),
		chromedp.OuterHTML("body", &pageHTML, chromedp.ByQuery),
	)
	if runError != nil {
		return nil, runError
	}

	document, documentError := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if documentError != nil {
		return nil, documentError
	}
	return parseDocument(document, pageURL), nil
}

func parseDocument(document *goquery.Document, pageURL string) []Session {
	var parsedSessions []Session
	document.Find(activityCardSelector).Each(func(_ int, card *goquery.Selection) {
		if card.Find("a").FilterFunction(func(_ int, link *goquery.Selection) bool {
			return strings.Contains(link.Text(), "View sub-activities")
		}).Length() > 0 {
			return
		}

		title := strings.TrimSpace(card.Find(activityTitleSelector).Text())
		dateText := strings.TrimSpace(card.Find(dateRangeSelector).Text())
		timeText := strings.TrimSpace(card.Find(timeRangeSelector).Text())
		ageText := strings.TrimSpace(card.Find(ageSelector).Text())

		var minimumAgePointer, maximumAgePointer *int
		if matches := ageRegularExpression.FindStringSubmatch(ageText); len(matches) == 3 {
			if value, _ := strconv.Atoi(matches[1]); value > 0 {
				minimumAgePointer = &value
			}
			if value, _ := strconv.Atoi(matches[2]); value > 0 {
				maximumAgePointer = &value
			}
		}

		availability := defaultAvailabilityLiteral
		if corner := card.Find(cornerMarkSelector); corner.Length() > 0 {
			availability = strings.TrimSpace(corner.Text())
		} else if alert := card.Find(alertTextSelector); alert.Length() > 0 {
			availability = strings.TrimSpace(alert.Text())
		}

		startDate, endDate := parseDateRangeText(dateText)
		startMinutes, endMinutes, dayTokens := splitTimeRangeText(timeText)

		parsedSessions = append(parsedSessions, Session{
			Title:         title,
			StartDateUnix: startDate.Unix(),
			EndDateUnix:   endDate.Unix(),
			Days:          dayTokens,
			StartMinutes:  startMinutes,
			EndMinutes:    endMinutes,
			MinAge:        minimumAgePointer,
			MaxAge:        maximumAgePointer,
			Availability:  availability,
			PageURL:       pageURL,
		})
	})
	return parsedSessions
}

func mustParse(layout, value string) time.Time {
	parsedTime, parseError := time.Parse(layout, value)
	if parseError != nil {
		panic(value)
	}
	return parsedTime
}

func parseDateRangeText(raw string) (time.Time, time.Time) {
	if strings.Contains(raw, "to") {
		parts := strings.Split(raw, "to")
		return mustParse("January 2, 2006", strings.TrimSpace(parts[0])),
			mustParse("January 2, 2006", strings.TrimSpace(parts[1]))
	}
	singleDate := mustParse("January 2, 2006", strings.TrimSpace(raw))
	return singleDate, singleDate
}

func splitTimeRangeText(source string) (int, int, []string) {
	spacePosition := strings.Index(source, " ")
	if spacePosition == -1 {
		return 0, 0, nil
	}
	dayToken := strings.TrimSpace(source[:spacePosition])
	timeRangeToken := strings.TrimSpace(source[spacePosition+1:])
	parts := strings.Split(timeRangeToken, "-")
	if len(parts) != 2 {
		return 0, 0, nil
	}
	startToken := strings.TrimSpace(parts[0])
	endToken := strings.TrimSpace(parts[1])
	startMinutes := normalizeClockToken(startToken, endToken)
	endMinutes := normalizeClockToken(endToken, startToken)
	return startMinutes, endMinutes, expandDayTokens(dayToken)
}

func normalizeClockToken(primary string, counterpart string) int {
	hasMeridian := func(value string) bool {
		upper := strings.ToUpper(value)
		return strings.Contains(upper, "AM") || strings.Contains(upper, "PM") || upper == "NOON"
	}

	primaryValue := strings.TrimSpace(primary)
	counterpartValue := strings.TrimSpace(counterpart)

	switch {
	case hasMeridian(primaryValue):
	case hasMeridian(counterpartValue):
		if strings.Contains(strings.ToUpper(counterpartValue), "AM") {
			primaryValue += " AM"
		} else {
			primaryValue += " PM"
		}
	default:
		if parsedTime, err := time.Parse("15:04", primaryValue); err == nil {
			return parsedTime.Hour()*60 + parsedTime.Minute()
		}
	}

	trialLayouts := []string{"3:04 PM", "3 PM", "3PM"}
	for _, layout := range trialLayouts {
		if parsedTime, err := time.Parse(layout, primaryValue); err == nil {
			return parsedTime.Hour()*60 + parsedTime.Minute()
		}
	}
	return 0
}

func expandDayTokens(token string) []string {
	var expanded []string
	for _, segment := range strings.Split(token, ",") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if strings.Contains(segment, "-") {
			rangeParts := strings.Split(segment, "-")
			startDay := strings.TrimSpace(rangeParts[0])
			endDay := strings.TrimSpace(rangeParts[1])
			startIndex, startOk := weekdayToIndexMap[startDay]
			endIndex, endOk := weekdayToIndexMap[endDay]
			if !startOk || !endOk {
				continue
			}
			for i := 0; ; i++ {
				index := (startIndex + i) % 7
				for dayName, dayIndex := range weekdayToIndexMap {
					if dayIndex == index {
						expanded = append(expanded, dayName)
						break
					}
				}
				if index == endIndex {
					break
				}
			}
		} else {
			expanded = append(expanded, segment)
		}
	}
	return expanded
}
