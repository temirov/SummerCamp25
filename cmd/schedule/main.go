// cmd/schedule/main.go
// Command schedule builds an optimized camp schedule for multiple children.
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
	emptyLiteral                           = ""
	availabilityAvailableLiteral           = "available"
	availabilityStartingSoonLiteral        = "starting soon"
	availabilitySpaceLeftIdentifierLiteral = "space"
	availabilityLeftIdentifierLiteral      = "left"
	bufferMinutesBetweenSessions           = 120
	jointScheduleHeadingLiteral            = "Joint schedule"
	childScheduleHeadingSuffixLiteral      = "schedule"
	flagSessionsParameterNameLiteral       = "sessions"
	flagWantParameterNameLiteral           = "want"
	flagJSONParameterNameLiteral           = "json"
	fatalMissingFlagsLiteral               = "FATAL: -sessions and -want are required"
	outputWrittenPrefixLiteral             = "wrote"
	dateLayoutISOLiteral                   = "2006-01-02"
	priorityHighLiteral                    = "High"
	priorityMediumLiteral                  = "Medium"
	priorityLowLiteral                     = "Low"
	priorityNoLiteral                      = "No"
)

var priorityScoreByWord = map[string]int{
	priorityHighLiteral:   3,
	priorityMediumLiteral: 2,
	priorityLowLiteral:    1,
	priorityNoLiteral:     0,
}

type Session struct {
	ActivityName         string            `json:"title"`
	StartDateUnixSeconds int64             `json:"startDateUnix"`
	EndDateUnixSeconds   int64             `json:"endDateUnix"`
	DaysOfWeek           []string          `json:"days"`
	StartTimeMilitary    string            `json:"startTime"`
	EndTimeMilitary      string            `json:"endTime"`
	MinimumAgeInclusive  *int              `json:"minAge"`
	MaximumAgeExclusive  *int              `json:"maxAge"`
	AvailabilityText     string            `json:"availability"`
	PageURL              string            `json:"pageUrl"`
	InterestedPriorities map[string]string `json:"interested"`
	startDate            time.Time
	endDate              time.Time
	startClockMinutes    int
	endClockMinutes      int
}

type wantFileData struct {
	childAgesByName        map[string]int
	childNamesSorted       []string
	sessionPriorityByChild map[string]map[string]string
}

type childPlan struct {
	scheduledSessions     []Session
	enrolledActivitiesSet map[string]struct{}
}

type simpleSessionJSON struct {
	Activity  string `json:"activity"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	URL       string `json:"url"`
}

type exportJSON struct {
	Joint    []simpleSessionJSON            `json:"joint"`
	Children map[string][]simpleSessionJSON `json:"children"`
}

// main entry
func main() {
	sessionsPathFlag := flag.String(flagSessionsParameterNameLiteral, emptyLiteral, emptyLiteral)
	wantPathFlag := flag.String(flagWantParameterNameLiteral, emptyLiteral, emptyLiteral)
	jsonOutputPathFlag := flag.String(flagJSONParameterNameLiteral, emptyLiteral, emptyLiteral)
	flag.Parse()

	if *sessionsPathFlag == emptyLiteral || *wantPathFlag == emptyLiteral {
		fmt.Println(fatalMissingFlagsLiteral)
		return
	}

	wantData := loadWantFile(*wantPathFlag)
	rawSessions := transformRawSessions(*sessionsPathFlag, wantData)
	optimizedPlans, jointSessions := buildOptimizedPlans(rawSessions, wantData)

	if *jsonOutputPathFlag != emptyLiteral {
		writeJSONOutput(*jsonOutputPathFlag, optimizedPlans, jointSessions, wantData.childNamesSorted)
		return
	}

	printTextOutput(jointSessions, optimizedPlans, wantData.childNamesSorted)
}

// loadWantFile parses want.csv.
func loadWantFile(wantCSVPath string) wantFileData {
	fileHandle, openError := os.Open(wantCSVPath)
	if openError != nil {
		panic(openError)
	}
	defer fileHandle.Close()

	csvReader := csv.NewReader(fileHandle)
	headerRow, _ := csvReader.Read()

	type columnIndices struct{ age, priority int }
	columnIndicesByChild := map[string]*columnIndices{}

	for columnIndex, headerValue := range headerRow {
		headerLower := strings.ToLower(headerValue)
		switch {
		case strings.Contains(headerLower, "'s age"):
			childName := strings.Trim(strings.Split(headerValue, "'")[0], "\" ")
			if columnIndicesByChild[childName] == nil {
				columnIndicesByChild[childName] = &columnIndices{}
			}
			columnIndicesByChild[childName].age = columnIndex
		case strings.Contains(headerLower, "'s priority"):
			childName := strings.Trim(strings.Split(headerValue, "'")[0], "\" ")
			if columnIndicesByChild[childName] == nil {
				columnIndicesByChild[childName] = &columnIndices{}
			}
			columnIndicesByChild[childName].priority = columnIndex
		}
	}

	childAgesByName := map[string]int{}
	sessionPriorityByChild := map[string]map[string]string{}

	for {
		row, readError := csvReader.Read()
		if readError != nil {
			break
		}
		sessionName := strings.TrimSpace(row[0])

		for childName, indices := range columnIndicesByChild {
			if childAgesByName[childName] == 0 && indices.age < len(row) {
				if parsedAge, parseError := strconv.Atoi(strings.TrimSpace(row[indices.age])); parseError == nil {
					childAgesByName[childName] = parsedAge
				}
			}

			priorityValue := priorityNoLiteral
			if indices.priority < len(row) {
				if priorityText := strings.TrimSpace(row[indices.priority]); priorityText != emptyLiteral {
					priorityValue = priorityText
				}
			}

			if sessionPriorityByChild[sessionName] == nil {
				sessionPriorityByChild[sessionName] = map[string]string{}
			}
			sessionPriorityByChild[sessionName][childName] = priorityValue
		}
	}

	var childNamesSorted []string
	for childName := range childAgesByName {
		childNamesSorted = append(childNamesSorted, childName)
	}
	sort.Strings(childNamesSorted)

	return wantFileData{
		childAgesByName:        childAgesByName,
		childNamesSorted:       childNamesSorted,
		sessionPriorityByChild: sessionPriorityByChild,
	}
}

// transformRawSessions converts scraper JSON to Session slice.
func transformRawSessions(jsonPath string, want wantFileData) []Session {
	jsonBytes, readError := os.ReadFile(jsonPath)
	if readError != nil {
		panic(readError)
	}

	var rawData []map[string]interface{}
	_ = json.Unmarshal(jsonBytes, &rawData)

	getStringField := func(m map[string]interface{}, key string) string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return emptyLiteral
	}

	getInt64Field := func(m map[string]interface{}, key string) int64 {
		if v, ok := m[key]; ok {
			switch tv := v.(type) {
			case float64:
				return int64(tv)
			case json.Number:
				i, _ := tv.Int64()
				return i
			}
		}
		return 0
	}

	var sessions []Session
	for _, rawEntry := range rawData {
		title := getStringField(rawEntry, "title")
		if title == emptyLiteral {
			continue
		}
		startUnix := getInt64Field(rawEntry, "startDateUnix")
		endUnix := getInt64Field(rawEntry, "endDateUnix")
		startTimeMilitary := getStringField(rawEntry, "startTime")
		endTimeMilitary := getStringField(rawEntry, "endTime")

		var daysSlice []string
		if rawDays, ok := rawEntry["days"].([]interface{}); ok {
			for _, dayInterface := range rawDays {
				if dayString, ok := dayInterface.(string); ok {
					daysSlice = append(daysSlice, dayString)
				}
			}
		}

		var minimumAgePointer, maximumAgePointer *int
		if value, ok := rawEntry["minAge"].(float64); ok {
			temp := int(value)
			minimumAgePointer = &temp
		}
		if value, ok := rawEntry["maxAge"].(float64); ok {
			temp := int(value)
			maximumAgePointer = &temp
		}

		availabilityText := getStringField(rawEntry, "availability")

		prioritiesMap := want.sessionPriorityByChild[title]
		if prioritiesMap == nil {
			prioritiesMap = map[string]string{}
		}

		session := Session{
			ActivityName:         title,
			StartDateUnixSeconds: startUnix,
			EndDateUnixSeconds:   endUnix,
			DaysOfWeek:           daysSlice,
			StartTimeMilitary:    startTimeMilitary,
			EndTimeMilitary:      endTimeMilitary,
			MinimumAgeInclusive:  minimumAgePointer,
			MaximumAgeExclusive:  maximumAgePointer,
			AvailabilityText:     availabilityText,
			PageURL:              getStringField(rawEntry, "pageUrl"),
			InterestedPriorities: prioritiesMap,
			startDate:            time.Unix(startUnix, 0),
			endDate:              time.Unix(endUnix, 0),
			startClockMinutes:    militaryTimeToMinutes(startTimeMilitary),
			endClockMinutes:      militaryTimeToMinutes(endTimeMilitary),
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// buildOptimizedPlans selects joint and individual sessions.
func buildOptimizedPlans(allSessions []Session, want wantFileData) (map[string]*childPlan, []Session) {
	plansByChild := map[string]*childPlan{}
	for _, childName := range want.childNamesSorted {
		plansByChild[childName] = &childPlan{enrolledActivitiesSet: map[string]struct{}{}}
	}

	type scoredSession struct {
		sessionInstance Session
		totalScore      int
	}

	var candidateJointSessions []scoredSession
	for _, session := range allSessions {
		if !availabilityIsOpen(session.AvailabilityText) {
			continue
		}
		totalPriorityScore := 0
		allChildrenInterested := true

		for _, childName := range want.childNamesSorted {
			priorityScore := priorityScoreByWord[session.InterestedPriorities[childName]]
			if priorityScore == 0 || !ageIsWithinBounds(want.childAgesByName[childName], session.MinimumAgeInclusive, session.MaximumAgeExclusive) {
				allChildrenInterested = false
				break
			}
			totalPriorityScore += priorityScore
		}

		if allChildrenInterested {
			candidateJointSessions = append(candidateJointSessions, scoredSession{sessionInstance: session, totalScore: totalPriorityScore})
		}
	}

	sort.Slice(candidateJointSessions, func(i, j int) bool {
		if candidateJointSessions[i].totalScore != candidateJointSessions[j].totalScore {
			return candidateJointSessions[i].totalScore > candidateJointSessions[j].totalScore
		}
		return candidateJointSessions[i].sessionInstance.startDate.Before(candidateJointSessions[j].sessionInstance.startDate)
	})

	var chosenJointSessions []Session
	for _, candidate := range candidateJointSessions {
		sessionFits := true
		for _, plan := range plansByChild {
			if !plan.sessionFitsInPlan(candidate.sessionInstance) {
				sessionFits = false
				break
			}
		}
		if sessionFits {
			chosenJointSessions = append(chosenJointSessions, candidate.sessionInstance)
			for _, plan := range plansByChild {
				plan.addSession(candidate.sessionInstance)
			}
		}
	}

	jointActivitySet := map[string]struct{}{}
	for _, session := range chosenJointSessions {
		jointActivitySet[session.ActivityName] = struct{}{}
	}

	type individualCandidate struct {
		sessionInstance Session
		childName       string
		priorityScore   int
	}

	var individualPool []individualCandidate
	for _, session := range allSessions {
		if !availabilityIsOpen(session.AvailabilityText) {
			continue
		}
		if _, alreadyJoint := jointActivitySet[session.ActivityName]; alreadyJoint {
			continue
		}
		for _, childName := range want.childNamesSorted {
			priorityScore := priorityScoreByWord[session.InterestedPriorities[childName]]
			if priorityScore == 0 || !ageIsWithinBounds(want.childAgesByName[childName], session.MinimumAgeInclusive, session.MaximumAgeExclusive) {
				continue
			}
			individualPool = append(individualPool, individualCandidate{sessionInstance: session, childName: childName, priorityScore: priorityScore})
		}
	}

	sort.Slice(individualPool, func(i, j int) bool {
		if individualPool[i].priorityScore != individualPool[j].priorityScore {
			return individualPool[i].priorityScore > individualPool[j].priorityScore
		}
		return individualPool[i].sessionInstance.startDate.Before(individualPool[j].sessionInstance.startDate)
	})

	for _, candidate := range individualPool {
		plan := plansByChild[candidate.childName]
		if plan.sessionFitsInPlan(candidate.sessionInstance) {
			plan.addSession(candidate.sessionInstance)
		}
	}

	return plansByChild, chosenJointSessions
}

// writeJSONOutput persists schedule to file.
func writeJSONOutput(outputPath string, plans map[string]*childPlan, jointSessions []Session, childNames []string) {
	exportData := exportJSON{Children: map[string][]simpleSessionJSON{}}

	sort.Slice(jointSessions, func(i, j int) bool { return jointSessions[i].startDate.Before(jointSessions[j].startDate) })
	for _, session := range jointSessions {
		exportData.Joint = append(exportData.Joint, simpleSessionJSON{
			Activity:  session.ActivityName,
			StartDate: session.startDate.Format(dateLayoutISOLiteral),
			EndDate:   session.endDate.Format(dateLayoutISOLiteral),
			URL:       session.PageURL,
		})
	}

	for _, childName := range childNames {
		plan := plans[childName]
		sort.Slice(plan.scheduledSessions, func(i, j int) bool {
			return plan.scheduledSessions[i].startDate.Before(plan.scheduledSessions[j].startDate)
		})
		for _, session := range plan.scheduledSessions {
			if _, sessionIsJoint := exportData.findJointActivity(session.ActivityName); sessionIsJoint {
				continue
			}
			exportData.Children[childName] = append(exportData.Children[childName], simpleSessionJSON{
				Activity:  session.ActivityName,
				StartDate: session.startDate.Format(dateLayoutISOLiteral),
				EndDate:   session.endDate.Format(dateLayoutISOLiteral),
				URL:       session.PageURL,
			})
		}
	}

	fileHandle, createError := os.Create(outputPath)
	if createError != nil {
		panic(createError)
	}
	defer fileHandle.Close()

	jsonEncoder := json.NewEncoder(fileHandle)
	jsonEncoder.SetIndent(emptyLiteral, "  ")
	_ = jsonEncoder.Encode(exportData)

	fmt.Println(outputWrittenPrefixLiteral, outputPath)
}

// printTextOutput prints schedule to stdout.
func printTextOutput(jointSessions []Session, plans map[string]*childPlan, childNames []string) {
	fmt.Println(jointScheduleHeadingLiteral)

	sort.Slice(jointSessions, func(i, j int) bool { return jointSessions[i].startDate.Before(jointSessions[j].startDate) })
	for _, session := range jointSessions {
		fmt.Println(session.ActivityName, session.startDate.Format(dateLayoutISOLiteral), session.endDate.Format(dateLayoutISOLiteral), session.PageURL)
	}

	fmt.Println()

	jointActivitySet := map[string]struct{}{}
	for _, session := range jointSessions {
		jointActivitySet[session.ActivityName] = struct{}{}
	}

	for _, childName := range childNames {
		plan := plans[childName]
		sort.Slice(plan.scheduledSessions, func(i, j int) bool {
			return plan.scheduledSessions[i].startDate.Before(plan.scheduledSessions[j].startDate)
		})
		fmt.Println(childName, childScheduleHeadingSuffixLiteral)
		for _, session := range plan.scheduledSessions {
			if _, sessionIsJoint := jointActivitySet[session.ActivityName]; sessionIsJoint {
				continue
			}
			fmt.Println(session.ActivityName, session.startDate.Format(dateLayoutISOLiteral), session.endDate.Format(dateLayoutISOLiteral), session.PageURL)
		}
		fmt.Println()
	}
}

func (plan *childPlan) sessionFitsInPlan(candidate Session) bool {
	if _, duplicate := plan.enrolledActivitiesSet[candidate.ActivityName]; duplicate {
		return false
	}
	for _, existing := range plan.scheduledSessions {
		if sessionsOverlap(existing, candidate) {
			return false
		}
	}
	return true
}

func (plan *childPlan) addSession(session Session) {
	plan.scheduledSessions = append(plan.scheduledSessions, session)
	plan.enrolledActivitiesSet[session.ActivityName] = struct{}{}
}

func (data exportJSON) findJointActivity(activityName string) (simpleSessionJSON, bool) {
	for _, joint := range data.Joint {
		if joint.Activity == activityName {
			return joint, true
		}
	}
	return simpleSessionJSON{}, false
}

func availabilityIsOpen(availabilityText string) bool {
	lower := strings.TrimSpace(strings.ToLower(availabilityText))
	switch lower {
	case emptyLiteral, availabilityAvailableLiteral, availabilityStartingSoonLiteral:
		return true
	default:
		return strings.Contains(lower, availabilitySpaceLeftIdentifierLiteral) && strings.Contains(lower, availabilityLeftIdentifierLiteral)
	}
}

func ageIsWithinBounds(age int, minPtr, maxPtr *int) bool {
	minimum := 0
	maximum := 1<<31 - 1
	if minPtr != nil {
		minimum = *minPtr
	}
	if maxPtr != nil {
		maximum = *maxPtr
	}
	return age >= minimum && age < maximum
}

func sessionsOverlap(sessionA, sessionB Session) bool {
	if sessionA.endDate.Before(sessionB.startDate) || sessionB.endDate.Before(sessionA.startDate) {
		return false
	}

	shareDay := false
	for _, dayA := range sessionA.DaysOfWeek {
		for _, dayB := range sessionB.DaysOfWeek {
			if dayA == dayB {
				shareDay = true
				break
			}
		}
		if shareDay {
			break
		}
	}
	if !shareDay {
		return false
	}

	if sessionA.endClockMinutes+bufferMinutesBetweenSessions <= sessionB.startClockMinutes ||
		sessionB.endClockMinutes+bufferMinutesBetweenSessions <= sessionA.startClockMinutes {
		return false
	}
	return true
}

func militaryTimeToMinutes(military string) int {
	parts := strings.Split(military, ":")
	if len(parts) != 2 {
		return 0
	}
	hours, _ := strconv.Atoi(parts[0])
	minutes, _ := strconv.Atoi(parts[1])
	return hours*60 + minutes
}
