package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

/*
   ───────────────────────────── data ─────────────────────────────
*/

type Raw struct {
	Title     string `json:"title"`
	DateRange string `json:"dateRange"`
	DayTime   string `json:"dayTime"`
	PageURL   string `json:"pageUrl"`
}

type Session struct {
	Activity     string          `json:"activity"`
	StartDate    string          `json:"startDate"`
	EndDate      string          `json:"endDate"`
	Days         []string        `json:"days"`
	StartTime    string          `json:"startTime"`
	EndTime      string          `json:"endTime"`
	Segment      string          `json:"segment"`
	Frequency    string          `json:"frequency"`
	ChildAllowed map[string]bool `json:"childAllowed"`
	PageURL      string          `json:"pageUrl"`
}

var allowed = map[string]map[string]bool{
	"Youth Beginner Ice Skating":                    {"Alice": false, "Peter": true},
	"Rocket Science & Astronomy!":                   {"Alice": false, "Peter": true},
	"REC Summer Field Trip wk 7: Disneyland":        {"Alice": true, "Peter": false},
	"REC Summer Field Trip wk 4: Universal Studios": {"Alice": true, "Peter": true},
	"Minecraft Modding":                             {"Alice": true, "Peter": true},
	"Minecraft Build & Design":                      {"Alice": true, "Peter": true},
	"Little Kings Hockey Skating":                   {"Alice": false, "Peter": true},
	"Kids Cooking Academy: Appetite For Adventure!": {"Alice": true, "Peter": false},
	"Gymnastics - Ninja Gym":                        {"Alice": false, "Peter": true},
	"Cooking with Disney":                           {"Alice": true, "Peter": false},
	"Chem Kidz!":                                    {"Alice": false, "Peter": true},
	"Camp Clay, Paint and Draw":                     {"Alice": true, "Peter": false},
	"Fortnite Brick Royale/Roblox":                  {"Alice": true, "Peter": true},
	"Animal Grossology":                             {"Alice": true, "Peter": false},
	"Beach Aqualetics":                              {"Alice": false, "Peter": true},
	"Battle Robots Building and Code":               {"Alice": false, "Peter": true},
}

/*
   ──────────────────────────── regexes ───────────────────────────
*/

var (
	reMonthDay = regexp.MustCompile(`([A-Za-z]+ \d{1,2}, \d{4})`)
	reDayBlock = regexp.MustCompile(`^([A-Za-z,–-]+)\s*(.*)$`)
	reTimeSpan = regexp.MustCompile(`([0-9]{1,2}:[0-9]{2}\s*[AP]M|Noon|Midnight)\s*-\s*([0-9]{1,2}:[0-9]{2}\s*[AP]M|Noon|Midnight)`)
	weekdaySeq = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	weekdaySet = map[string]struct{}{"Mon": {}, "Tue": {}, "Wed": {}, "Thu": {}, "Fri": {}, "Sat": {}, "Sun": {}}
)

/*
   ──────────────────────────── helpers ───────────────────────────
*/

func canonicalClock(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "noon":
		return "12:00 PM"
	case "midnight":
		return "12:00 AM"
	default:
		return t
	}
}

func parseClock(s string) (time.Time, bool) {
	for _, layout := range []string{"3:04 PM", "3:04PM"} {
		if d, err := time.Parse(layout, strings.ToUpper(s)); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

func segmentLabel(s, e time.Time) string {
	switch {
	case s.Hour() >= 5 && e.Hour() <= 12:
		return "morning"
	case s.Hour() >= 12 && e.Hour() <= 17:
		return "afternoon"
	default:
		return "allday"
	}
}

func uniq(in []string) []string {
	set := make(map[string]struct{}, len(in))
	var out []string
	for _, d := range in {
		if _, ok := set[d]; !ok {
			set[d] = struct{}{}
			out = append(out, d)
		}
	}
	return out
}

func daysFrom(block string) []string {
	block = strings.ReplaceAll(block, " ", "")
	var out []string
	for _, seg := range strings.Split(block, ",") {
		if strings.ContainsAny(seg, "–-") { // range
			sep := strings.IndexAny(seg, "–-")
			a, b := strings.Title(seg[:3]), strings.Title(seg[sep+1:sep+4])
			ai, bi := -1, -1
			for i, w := range weekdaySeq {
				if w == a {
					ai = i
				}
				if w == b {
					bi = i
				}
			}
			if ai >= 0 && bi >= ai {
				out = append(out, weekdaySeq[ai:bi+1]...)
			}
		} else { // single day
			w := strings.Title(seg[:3])
			if _, ok := weekdaySet[w]; ok {
				out = append(out, w)
			}
		}
	}
	return uniq(out)
}

func frequency(days []string) string {
	switch len(days) {
	case 0:
		return "unspecified"
	case 1:
		return "weekly_once"
	case 5:
		if containsAll(days, []string{"Mon", "Tue", "Wed", "Thu", "Fri"}) {
			return "weekdays"
		}
	}
	return "weekly_multi"
}

func containsAll(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, d := range have {
		set[d] = struct{}{}
	}
	for _, d := range want {
		if _, ok := set[d]; !ok {
			return false
		}
	}
	return true
}

func validCalDate(s string) bool {
	_, err := time.Parse("January 2, 2006", s)
	return err == nil
}

/*
   ───────────────────────────── main ─────────────────────────────
*/

func main() {
	log.SetFlags(0)
	var merged []Session
	var warnings []string

	_ = filepath.WalkDir("scrapes", func(p string, d fs.DirEntry, _ error) error {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		var raws []Raw
		if err := json.Unmarshal(b, &raws); err != nil {
			return err
		}

		for _, r := range raws {
			perm, ok := allowed[r.Title]
			if !ok {
				continue
			}

			/* ---------------- dates ---------------- */
			startDate, endDate := "", ""
			if ds := reMonthDay.FindAllString(r.DateRange, -1); len(ds) == 2 {
				startDate, endDate = ds[0], ds[1]
			} else if len(ds) == 1 {
				startDate, endDate = ds[0], ds[0]
			}

			if startDate != "" && !validCalDate(startDate) {
				warnings = append(warnings, fmt.Sprintf("invalid start date %q ‒ %q", startDate, r.Title))
				continue
			}
			if endDate != "" && !validCalDate(endDate) {
				warnings = append(warnings, fmt.Sprintf("invalid end date %q ‒ %q", endDate, r.Title))
				continue
			}

			/* --------------- days/time -------------- */
			days, stime, etime, seg := []string{}, "", "", "unspecified"
			timeSpanOK := false

			if m := reDayBlock.FindStringSubmatch(r.DayTime); len(m) == 3 {
				days = daysFrom(m[1])

				if ts := reTimeSpan.FindStringSubmatch(m[2]); len(ts) == 3 {
					stime, etime = canonicalClock(ts[1]), canonicalClock(ts[2])

					sc, ok1 := parseClock(stime)
					ec, ok2 := parseClock(etime)
					if ok1 && ok2 {
						seg = segmentLabel(sc, ec)
						timeSpanOK = true
					} else {
						warnings = append(warnings, fmt.Sprintf("unparseable time span %q ‒ %q", m[2], r.Title))
						continue
					}
				}
			}

			/* reject entries that have neither date range nor valid time span */
			if startDate == "" && !timeSpanOK {
				warnings = append(warnings, fmt.Sprintf("no calendar data ‒ %q", r.Title))
				continue
			}

			merged = append(merged, Session{
				Activity:     r.Title,
				StartDate:    startDate,
				EndDate:      endDate,
				Days:         days,
				StartTime:    stime,
				EndTime:      etime,
				Segment:      seg,
				Frequency:    frequency(days),
				ChildAllowed: perm,
				PageURL:      r.PageURL,
			})
		}
		return nil
	})

	/* write sessions.json WITHOUT HTML-escaping (& stays & not \u0026) */
	f, err := os.Create("sessions.json")
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(merged); err != nil {
		log.Fatal(err)
	}
	_ = f.Close()

	for _, w := range warnings {
		log.Println("⚠️ ", w)
	}
	log.Printf("✅  %d sessions written to sessions.json\n", len(merged))
}
