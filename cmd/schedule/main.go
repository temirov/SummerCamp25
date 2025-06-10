// cmd/schedule/main.go
// Package main schedules camp sessions for multiple children and prints joint and individual plans.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dateLayoutMonthDayYear          = "January 2, 2006"
	clockLayoutHourMinuteAMPM       = "3:04 PM"
	noonLiteral                     = "Noon"
	emptyLiteral                    = ""
	availabilityAvailable           = "available"
	availabilityStartingSoon        = "starting soon"
	availabilitySpaceLeftIdentifier = "space"
	availabilityLeftIdentifier      = "left"
	meridiemAM                      = "AM"
	meridiemPM                      = "PM"
)

var priorityScoreByWord = map[string]int{
	"High":   3,
	"Medium": 2,
	"Low":    1,
	"No":     0,
}

// Session is a camp session enriched with scheduling metadata.
type Session struct {
	ID           int               `json:"id"`
	Activity     string            `json:"activity"`
	StartDate    string            `json:"startDate"`
	EndDate      string            `json:"endDate"`
	StartTime    string            `json:"startTime"`
	EndTime      string            `json:"endTime"`
	Days         []string          `json:"days"`
	PageURL      string            `json:"pageUrl"`
	MinAge       *int              `json:"minAge"`
	MaxAge       *int              `json:"maxAge"`
	Availability string            `json:"availability"`
	Interested   map[string]string `json:"interested"`
	start        time.Time
	end          time.Time
}

func mustParse(layout, value string) time.Time {
	parsed, err := time.Parse(layout, value)
	if err != nil {
		panic(fmt.Sprintf("cannot parse %q with %q: %v", value, layout, err))
	}
	return parsed
}

func clockMinutes(clock string) int {
	if clock == emptyLiteral {
		return 0
	}
	if strings.EqualFold(clock, noonLiteral) {
		return 12 * 60
	}
	parsed := mustParse(clockLayoutHourMinuteAMPM, clock)
	return parsed.Hour()*60 + parsed.Minute()
}

func splitTimeRange(dayAndTime string) (startMinutes, endMinutes int, days []string) {
	parts := strings.Split(dayAndTime, " ")
	if len(parts) < 3 {
		return
	}
	dayPart := parts[0]
	timePart := strings.Join(parts[1:], " ")
	days = strings.Split(dayPart, ",")
	timeParts := strings.Split(timePart, "-")
	if len(timeParts) != 2 {
		return
	}
	startMinutes = clockMinutes(strings.TrimSpace(timeParts[0]))
	endMinutes = clockMinutes(strings.TrimSpace(timeParts[1]))
	return
}

func parseDateRange(dateRange string) (start, end string) {
	if strings.Contains(dateRange, "to") {
		bounds := strings.Split(dateRange, "to")
		start = strings.TrimSpace(bounds[0])
		end = strings.TrimSpace(bounds[1])
		return
	}
	start = strings.TrimSpace(dateRange)
	end = start
	return
}

type wantData struct {
	childAges         map[string]int
	childNames        []string
	sessionPriorities map[string]map[string]string
}

func loadWant(csvPath string) wantData {
	fileHandle, err := os.Open(csvPath)
	if err != nil {
		panic(err)
	}
	defer fileHandle.Close()

	reader := csv.NewReader(fileHandle)
	headerRow, _ := reader.Read()

	type columnPair struct{ ageColumn, priorityColumn int }
	childColumns := map[string]*columnPair{}
	for columnIndex, header := range headerRow {
		lowered := strings.ToLower(header)
		if strings.Contains(lowered, "'s age") {
			childName := strings.Trim(strings.Split(header, "'")[0], `" `)
			if childColumns[childName] == nil {
				childColumns[childName] = &columnPair{}
			}
			childColumns[childName].ageColumn = columnIndex
		} else if strings.Contains(lowered, "'s priority") {
			childName := strings.Trim(strings.Split(header, "'")[0], `" `)
			if childColumns[childName] == nil {
				childColumns[childName] = &columnPair{}
			}
			childColumns[childName].priorityColumn = columnIndex
		}
	}

	childAges := map[string]int{}
	sessionPriorities := map[string]map[string]string{}
	for {
		row, err := reader.Read()
		if err != nil {
			break
		}
		sessionName := strings.TrimSpace(row[0])
		for childName, columns := range childColumns {
			ageString := strings.TrimSpace(row[columns.ageColumn])
			if childAges[childName] == 0 && ageString != emptyLiteral {
				if ageValue, err := strconv.Atoi(ageString); err == nil {
					childAges[childName] = ageValue
				}
			}
			priority := "No"
			if columns.priorityColumn < len(row) {
				priorityValue := strings.TrimSpace(row[columns.priorityColumn])
				if priorityValue != emptyLiteral {
					priority = priorityValue
				}
			}
			if sessionPriorities[sessionName] == nil {
				sessionPriorities[sessionName] = map[string]string{}
			}
			sessionPriorities[sessionName][childName] = priority
		}
	}

	names := make([]string, 0, len(childAges))
	for name := range childAges {
		names = append(names, name)
	}
	sort.Strings(names)
	return wantData{childAges: childAges, childNames: names, sessionPriorities: sessionPriorities}
}

func transformRawSessions(rawPath string, want wantData) []Session {
	var rawInput []map[string]interface{}
	rawBytes, _ := os.ReadFile(rawPath)
	_ = json.Unmarshal(rawBytes, &rawInput)

	var sessions []Session
	for index, item := range rawInput {
		title := item["title"].(string)
		dateRange := item["dateRange"].(string)
		dayAndTime := item["dayTime"].(string)

		startDateString, endDateString := parseDateRange(dateRange)
		startMinutes, endMinutes, days := splitTimeRange(dayAndTime)

		var minimumAgePointer, maximumAgePointer *int
		if value, ok := item["minAge"].(float64); ok {
			integerValue := int(value)
			minimumAgePointer = &integerValue
		}
		if value, ok := item["maxAge"].(float64); ok {
			integerValue := int(value)
			maximumAgePointer = &integerValue
		}
		pageURL := item["pageUrl"].(string)
		availabilityString := emptyLiteral
		if v, ok := item["availability"].(string); ok {
			availabilityString = v
		}

		session := Session{
			ID:           index,
			Activity:     title,
			StartDate:    startDateString,
			EndDate:      endDateString,
			StartTime:    minutesToClock(startMinutes),
			EndTime:      minutesToClock(endMinutes),
			Days:         days,
			PageURL:      pageURL,
			MinAge:       minimumAgePointer,
			MaxAge:       maximumAgePointer,
			Availability: availabilityString,
			Interested:   want.sessionPriorities[title],
		}
		if session.StartDate != emptyLiteral {
			sessionDay := mustParse(dateLayoutMonthDayYear, session.StartDate)
			session.start = sessionDay.Add(time.Duration(startMinutes) * time.Minute)
			session.end = sessionDay.Add(time.Duration(endMinutes) * time.Minute)
		}
		sessions = append(sessions, session)
	}
	return sessions
}

func minutesToClock(totalMinutes int) string {
	if totalMinutes == 0 {
		return emptyLiteral
	}
	if totalMinutes == 12*60 {
		return "12:00 PM"
	}
	hour := totalMinutes / 60
	minute := totalMinutes % 60
	meridiem := meridiemAM
	if hour >= 12 {
		meridiem = meridiemPM
	}
	if hour > 12 {
		hour -= 12
	}
	return fmt.Sprintf("%d:%02d %s", hour, minute, meridiem)
}

type childPlan struct {
	name         string
	scheduled    []Session
	enrolledActs map[string]struct{}
}

func (plan *childPlan) isFree(session Session) bool {
	if session.start.IsZero() {
		return false
	}
	if _, already := plan.enrolledActs[session.Activity]; already {
		return false
	}
	buffer := 30 * time.Minute
	for _, existing := range plan.scheduled {
		if existing.start.IsZero() {
			continue
		}
		if session.start.Add(buffer).Before(existing.start) || existing.end.Add(buffer).Before(session.start) {
			continue
		}
		if session.start.Day() == existing.start.Day() && session.start.Month() == existing.start.Month() {
			return false
		}
	}
	return true
}

func availabilityIsOpen(status string) bool {
	lowered := strings.TrimSpace(strings.ToLower(status))
	switch {
	case lowered == emptyLiteral, lowered == availabilityAvailable, lowered == availabilityStartingSoon:
		return true
	case strings.Contains(lowered, availabilitySpaceLeftIdentifier) && strings.Contains(lowered, availabilityLeftIdentifier):
		return true
	default:
		return false
	}
}

func main() {
	sessionsPathFlag := flag.String("sessions", emptyLiteral, "raw sessions JSON")
	wantPathFlag := flag.String("want", emptyLiteral, "want.csv")
	flag.Parse()
	if *sessionsPathFlag == emptyLiteral || *wantPathFlag == emptyLiteral {
		fmt.Println("FATAL: both -sessions and -want required")
		return
	}

	want := loadWant(*wantPathFlag)
	sessions := transformRawSessions(*sessionsPathFlag, want)

	childPlans := map[string]*childPlan{}
	for _, childName := range want.childNames {
		childPlans[childName] = &childPlan{name: childName, enrolledActs: map[string]struct{}{}}
	}

	type assignment struct {
		session  Session
		child    string
		priority int
	}
	var assignmentPool []assignment
	for _, session := range sessions {
		if !availabilityIsOpen(session.Availability) {
			continue
		}
		for _, childName := range want.childNames {
			childAge := want.childAges[childName]
			if childAge < getIntFromPointer(session.MinAge) || childAge >= getIntFromPointer(session.MaxAge) {
				continue
			}
			priority := priorityScoreByWord[session.Interested[childName]]
			if priority > 0 {
				assignmentPool = append(assignmentPool, assignment{session: session, child: childName, priority: priority})
			}
		}
	}

	sort.Slice(assignmentPool, func(i, j int) bool {
		if assignmentPool[i].priority != assignmentPool[j].priority {
			return assignmentPool[i].priority > assignmentPool[j].priority
		}
		return assignmentPool[i].session.start.Before(assignmentPool[j].session.start)
	})

	for _, assign := range assignmentPool {
		plan := childPlans[assign.child]
		if plan.isFree(assign.session) {
			plan.scheduled = append(plan.scheduled, assign.session)
			plan.enrolledActs[assign.session.Activity] = struct{}{}
		}
	}

	// Joint sessions
	jointCountByID := map[int]int{}
	for _, plan := range childPlans {
		for _, session := range plan.scheduled {
			jointCountByID[session.ID]++
		}
	}
	var jointSessions []Session
	for sessionID, count := range jointCountByID {
		if count == len(want.childNames) {
			// pick session from any child plan
			for _, plan := range childPlans {
				for _, s := range plan.scheduled {
					if s.ID == sessionID {
						jointSessions = append(jointSessions, s)
						break
					}
				}
				break
			}
		}
	}
	sort.Slice(jointSessions, func(i, j int) bool {
		return jointSessions[i].start.Before(jointSessions[j].start)
	})

	fmt.Println("Joint schedule")
	for _, s := range jointSessions {
		fmt.Println(s.Activity, s.StartDate, s.EndDate, s.PageURL)
	}
	fmt.Println()

	for _, childName := range want.childNames {
		plan := childPlans[childName]
		sort.Slice(plan.scheduled, func(i, j int) bool {
			return plan.scheduled[i].start.Before(plan.scheduled[j].start)
		})
		fmt.Println(childName, "schedule")
		for _, session := range plan.scheduled {
			fmt.Println(session.Activity, session.StartDate, session.EndDate, session.PageURL)
		}
		fmt.Println()
	}
}

func getIntFromPointer(pointer *int) int {
	if pointer == nil {
		return 1<<31 - 1
	}
	return *pointer
}
