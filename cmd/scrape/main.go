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

// Session describes a scraped activity session.
type Session struct {
	Title        string `json:"title"`
	DateRange    string `json:"dateRange"`
	DayTime      string `json:"dayTime"`
	MinAge       *int   `json:"minAge"`
	MaxAge       *int   `json:"maxAge"`
	Availability string `json:"availability"`
	PageURL      string `json:"pageUrl"`
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

	campNames, loadError := loadCampNames(*csvFilePath)
	if loadError != nil {
		log.Fatalf("FATAL: loading CSV %q: %v", *csvFilePath, loadError)
	}
	if len(campNames) == 0 {
		log.Fatalf("FATAL: no camp names found in %s", *csvFilePath)
	}

	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromeExecutablePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...,
	)
	defer cancelAllocator()

	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()

	if startupError := chromedp.Run(browserContext); startupError != nil {
		log.Fatalf("FATAL: starting Chrome: %v", startupError)
	}

	var combinedSessions []Session
	for _, campName := range campNames {
		escapedName := url.QueryEscape(campName)
		navigationURL := fmt.Sprintf(baseSearchURL, escapedName)
		log.Printf("Scraping %q → %s", campName, navigationURL)

		scrapedSessions, scrapeError := scrapePage(browserContext, navigationURL)
		if scrapeError != nil {
			log.Printf("  → ERROR scraping %q: %v", campName, scrapeError)
			continue
		}
		combinedSessions = append(combinedSessions, scrapedSessions...)
		time.Sleep(postScrapeSleepDuration)
	}

	outputFileHandle, createError := os.Create(*outputFilePath)
	if createError != nil {
		log.Fatalf("FATAL: creating %s: %v", *outputFilePath, createError)
	}
	defer outputFileHandle.Close()

	jsonEncoder := json.NewEncoder(outputFileHandle)
	jsonEncoder.SetEscapeHTML(false)
	jsonEncoder.SetIndent("", "  ")
	if encodeError := jsonEncoder.Encode(combinedSessions); encodeError != nil {
		log.Fatalf("FATAL: writing JSON: %v", encodeError)
	}

	log.Printf("Done: wrote %d sessions to %s", len(combinedSessions), *outputFilePath)
}

func loadCampNames(csvFilePath string) ([]string, error) {
	fileReader, openError := os.Open(csvFilePath)
	if openError != nil {
		return nil, openError
	}
	defer fileReader.Close()

	csvReader := csv.NewReader(fileReader)
	headerColumns, readHeaderError := csvReader.Read()
	if readHeaderError != nil {
		return nil, readHeaderError
	}

	campColumnIndex := -1
	for columnIndex, columnName := range headerColumns {
		if columnName == "Camp" {
			campColumnIndex = columnIndex
			break
		}
	}
	if campColumnIndex < 0 {
		return nil, fmt.Errorf("no 'Camp' column in header: %v", headerColumns)
	}

	var campNames []string
	for {
		csvRecord, recordError := csvReader.Read()
		if recordError != nil {
			if recordError == io.EOF {
				break
			}
			return nil, recordError
		}
		if campColumnIndex < len(csvRecord) {
			trimmedName := strings.TrimSpace(csvRecord[campColumnIndex])
			if trimmedName != "" {
				campNames = append(campNames, trimmedName)
			}
		}
	}
	return campNames, nil
}

func scrapePage(parentContext context.Context, pageURL string) ([]Session, error) {
	scrapeContext, cancelScrape := context.WithTimeout(parentContext, scrapeTimeout)
	defer cancelScrape()

	var pageHTML string
	runError := chromedp.Run(scrapeContext,
		chromedp.Navigate(pageURL),
		chromedp.WaitVisible(activityCardSelector, chromedp.ByQuery),
		chromedp.Sleep(postClickSleepDuration),
		chromedp.ActionFunc(func(actionContext context.Context) error {
			clickError := chromedp.Click(subActivitiesSelector, chromedp.BySearch, chromedp.AtLeast(0)).Do(actionContext)
			if clickError != nil {
				log.Printf("    Info: no %q link found: %v", subActivitiesLinkText, clickError)
			}
			return nil
		}),
		chromedp.Sleep(postClickSleepDuration),
		chromedp.OuterHTML(bodySelector, &pageHTML, chromedp.ByQuery),
	)
	if runError != nil {
		return nil, fmt.Errorf("chromedp run error: %w", runError)
	}

	document, parseError := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if parseError != nil {
		return nil, fmt.Errorf("parsing HTML: %w", parseError)
	}

	var scrapedSessions []Session
	document.Find(activityCardSelector).Each(func(itemIndex int, cardSelection *goquery.Selection) {
		if cardSelection.Find("a").FilterFunction(func(innerIndex int, innerSelection *goquery.Selection) bool {
			return strings.Contains(innerSelection.Text(), subActivitiesLinkText)
		}).Length() > 0 {
			return
		}

		titleText := strings.TrimSpace(cardSelection.Find(titleSelector).Text())
		dateText := strings.TrimSpace(cardSelection.Find(dateRangeSelector).Text())
		timeText := strings.TrimSpace(cardSelection.Find(timeRangeSelector).Text())
		ageText := strings.TrimSpace(cardSelection.Find(ageSelector).Text())

		var minimumAgeValue *int
		var maximumAgeValue *int
		if regexMatches := ageRegex.FindStringSubmatch(ageText); len(regexMatches) == 3 {
			if parsedMinAge, parseMinimumError := strconv.Atoi(regexMatches[1]); parseMinimumError == nil {
				minimumAgeValue = &parsedMinAge
			}
			if parsedMaxAge, parseMaximumError := strconv.Atoi(regexMatches[2]); parseMaximumError == nil {
				maximumAgeValue = &parsedMaxAge
			}
		}

		availabilityValue := defaultAvailability
		if cornerSelection := cardSelection.Find(cornerMarkSelector); cornerSelection.Length() > 0 {
			availabilityValue = strings.TrimSpace(cornerSelection.Text())
		} else if alertSelection := cardSelection.Find(alertTextSelector); alertSelection.Length() > 0 {
			availabilityValue = strings.TrimSpace(alertSelection.Text())
		}

		scrapedSessions = append(scrapedSessions, Session{
			Title:        titleText,
			DateRange:    dateText,
			DayTime:      timeText,
			MinAge:       minimumAgeValue,
			MaxAge:       maximumAgeValue,
			Availability: availabilityValue,
			PageURL:      pageURL,
		})
	})

	return scrapedSessions, nil
}
