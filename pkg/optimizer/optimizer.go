// pkg/optimizer/optimizer.go
package optimizer

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"SummerCamp25/pkg/log"
	"SummerCamp25/pkg/scraper"
	"go.uber.org/zap"
)

const (
	campColumnHeaderLiteral         = "Camp"
	csvFieldEmptyLiteral            = ""
	jsonIndentPrefixLiteral         = ""
	jsonIndentLiteral               = "  "
	errorNoCampColumnLiteral        = "no Camp column in CSV header"
	bufferMinutesBetweenSessions    = 120
	availabilityAvailableLiteral    = "available"
	availabilityStartingSoonLiteral = "starting soon"
	availabilitySpaceLeftIdentifier = "space"
	availabilityLeftIdentifier      = "left"
	priorityHighLiteral             = "High"
	priorityMediumLiteral           = "Medium"
	priorityLowLiteral              = "Low"
	priorityNoLiteral               = "No"
	isoDateLayoutLiteral            = "2006-01-02"
)

var priorityScoreByWord = map[string]int{
	priorityHighLiteral:   3,
	priorityMediumLiteral: 2,
	priorityLowLiteral:    1,
	priorityNoLiteral:     0,
}

type wantFileData struct {
	childAgesByName        map[string]int
	childNamesSorted       []string
	sessionPriorityByChild map[string]map[string]string
}

type session struct {
	activityName         string
	startDate            time.Time
	endDate              time.Time
	daysOfWeek           []string
	startClockMinutes    int
	endClockMinutes      int
	minimumAgeInclusive  *int
	maximumAgeExclusive  *int
	availabilityText     string
	pageURL              string
	interestedPriorities map[string]string
}

type childPlan struct {
	scheduledSessions     []session
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

func ExtractCampNames(csvBytes []byte) ([]string, error) {
	csvReader := csv.NewReader(bytes.NewReader(csvBytes))
	headerRow, headerError := csvReader.Read()
	if headerError != nil {
		return nil, headerError
	}
	campColumnIndex := -1
	for index, header := range headerRow {
		if strings.TrimSpace(header) == campColumnHeaderLiteral {
			campColumnIndex = index
			break
		}
	}
	if campColumnIndex < 0 {
		return nil, errors.New(errorNoCampColumnLiteral)
	}
	uniqueCampSet := map[string]struct{}{}
	for {
		row, readError := csvReader.Read()
		if readError != nil {
			break
		}
		if campColumnIndex < len(row) {
			if value := strings.TrimSpace(row[campColumnIndex]); value != "" {
				uniqueCampSet[value] = struct{}{}
			}
		}
	}
	var campNames []string
	for camp := range uniqueCampSet {
		campNames = append(campNames, camp)
	}
	sort.Strings(campNames)
	return campNames, nil
}

func Optimize(csvBytes []byte, scrapedSessions []scraper.Session) ([]byte, error) {
	log.L().Info("optimize_start", zap.Int("scraped_sessions", len(scrapedSessions)))
	wantData := loadWantFileData(csvBytes)
	internalSessions := transformScraperSessions(scrapedSessions, wantData)
	plansByChild, jointSessions := buildOptimizedPlans(internalSessions, wantData)

	exportData := exportJSON{Children: map[string][]simpleSessionJSON{}}

	sort.Slice(jointSessions, func(i, j int) bool { return jointSessions[i].startDate.Before(jointSessions[j].startDate) })
	for _, js := range jointSessions {
		exportData.Joint = append(exportData.Joint, simpleSessionJSON{
			Activity:  js.activityName,
			StartDate: js.startDate.Format(isoDateLayoutLiteral),
			EndDate:   js.endDate.Format(isoDateLayoutLiteral),
			URL:       js.pageURL,
		})
	}

	for _, child := range wantData.childNamesSorted {
		childPlan := plansByChild[child]
		sort.Slice(childPlan.scheduledSessions, func(i, j int) bool {
			return childPlan.scheduledSessions[i].startDate.Before(childPlan.scheduledSessions[j].startDate)
		})
		for _, sessionInstance := range childPlan.scheduledSessions {
			if _, isJoint := exportData.findJointActivity(sessionInstance.activityName); isJoint {
				continue
			}
			exportData.Children[child] = append(exportData.Children[child], simpleSessionJSON{
				Activity:  sessionInstance.activityName,
				StartDate: sessionInstance.startDate.Format(isoDateLayoutLiteral),
				EndDate:   sessionInstance.endDate.Format(isoDateLayoutLiteral),
				URL:       sessionInstance.pageURL,
			})
		}
	}

	encoded, marshalError := json.MarshalIndent(exportData, jsonIndentPrefixLiteral, jsonIndentLiteral)
	if marshalError != nil {
		return nil, marshalError
	}
	log.L().Info("optimize_done", zap.Int("joint", len(exportData.Joint)))
	return encoded, nil
}

func buildOptimizedPlans(allSessions []session, want wantFileData) (map[string]*childPlan, []session) {
	plansByChild := map[string]*childPlan{}
	for _, child := range want.childNamesSorted {
		plansByChild[child] = &childPlan{enrolledActivitiesSet: map[string]struct{}{}}
	}

	type scoredSession struct {
		sessionInstance session
		totalScore      int
	}

	var candidateJointSessions []scoredSession
	for _, sessionInstance := range allSessions {
		if !availabilityIsOpen(sessionInstance.availabilityText) {
			continue
		}
		totalScore := 0
		allChildrenInterested := true

		for _, child := range want.childNamesSorted {
			score := priorityScoreByWord[sessionInstance.interestedPriorities[child]]
			if score == 0 || !ageIsWithinBounds(want.childAgesByName[child], sessionInstance.minimumAgeInclusive, sessionInstance.maximumAgeExclusive) {
				allChildrenInterested = false
				break
			}
			totalScore += score
		}

		if allChildrenInterested {
			candidateJointSessions = append(candidateJointSessions, scoredSession{sessionInstance: sessionInstance, totalScore: totalScore})
		}
	}

	sort.Slice(candidateJointSessions, func(i, j int) bool {
		if candidateJointSessions[i].totalScore != candidateJointSessions[j].totalScore {
			return candidateJointSessions[i].totalScore > candidateJointSessions[j].totalScore
		}
		return candidateJointSessions[i].sessionInstance.startDate.Before(candidateJointSessions[j].sessionInstance.startDate)
	})

	var chosenJointSessions []session
	for _, candidate := range candidateJointSessions {
		conflicts := false
		for _, plan := range plansByChild {
			if !plan.sessionFits(candidate.sessionInstance) {
				conflicts = true
				break
			}
		}
		if !conflicts {
			chosenJointSessions = append(chosenJointSessions, candidate.sessionInstance)
			for _, plan := range plansByChild {
				plan.addSession(candidate.sessionInstance)
			}
		}
	}

	jointActivitySet := map[string]struct{}{}
	for _, js := range chosenJointSessions {
		jointActivitySet[js.activityName] = struct{}{}
	}

	type individualCandidate struct {
		sessionInstance session
		childName       string
		priorityScore   int
	}

	var individualPool []individualCandidate
	for _, sessionInstance := range allSessions {
		if !availabilityIsOpen(sessionInstance.availabilityText) {
			continue
		}
		if _, alreadyJoint := jointActivitySet[sessionInstance.activityName]; alreadyJoint {
			continue
		}
		for _, child := range want.childNamesSorted {
			score := priorityScoreByWord[sessionInstance.interestedPriorities[child]]
			if score == 0 || !ageIsWithinBounds(want.childAgesByName[child], sessionInstance.minimumAgeInclusive, sessionInstance.maximumAgeExclusive) {
				continue
			}
			individualPool = append(individualPool, individualCandidate{sessionInstance: sessionInstance, childName: child, priorityScore: score})
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
		if plan.sessionFits(candidate.sessionInstance) {
			plan.addSession(candidate.sessionInstance)
		}
	}
	return plansByChild, chosenJointSessions
}

func availabilityIsOpen(availabilityText string) bool {
	lower := strings.TrimSpace(strings.ToLower(availabilityText))
	switch lower {
	case csvFieldEmptyLiteral, availabilityAvailableLiteral, availabilityStartingSoonLiteral:
		return true
	default:
		return strings.Contains(lower, availabilitySpaceLeftIdentifier) && strings.Contains(lower, availabilityLeftIdentifier)
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

func (plan *childPlan) sessionFits(candidate session) bool {
	if _, duplicate := plan.enrolledActivitiesSet[candidate.activityName]; duplicate {
		return false
	}
	for _, existing := range plan.scheduledSessions {
		if sessionsOverlap(existing, candidate) {
			return false
		}
	}
	return true
}

func (plan *childPlan) addSession(newSession session) {
	plan.scheduledSessions = append(plan.scheduledSessions, newSession)
	plan.enrolledActivitiesSet[newSession.activityName] = struct{}{}
}

func (data exportJSON) findJointActivity(activityName string) (simpleSessionJSON, bool) {
	for _, joint := range data.Joint {
		if joint.Activity == activityName {
			return joint, true
		}
	}
	return simpleSessionJSON{}, false
}

func transformScraperSessions(source []scraper.Session, want wantFileData) []session {
	var transformed []session
	for _, s := range source {
		priorities := want.sessionPriorityByChild[s.Title]
		if priorities == nil {
			priorities = map[string]string{}
		}
		transformed = append(transformed, session{
			activityName:         s.Title,
			startDate:            time.Unix(s.StartDateUnix, 0),
			endDate:              time.Unix(s.EndDateUnix, 0),
			daysOfWeek:           s.Days,
			startClockMinutes:    s.StartMinutes,
			endClockMinutes:      s.EndMinutes,
			minimumAgeInclusive:  s.MinAge,
			maximumAgeExclusive:  s.MaxAge,
			availabilityText:     s.Availability,
			pageURL:              s.PageURL,
			interestedPriorities: priorities,
		})
	}
	return transformed
}

func sessionsOverlap(a, b session) bool {
	if a.endDate.Before(b.startDate) || b.endDate.Before(a.startDate) {
		return false
	}
	shareDay := false
	if len(a.daysOfWeek) == 0 || len(b.daysOfWeek) == 0 {
		shareDay = true
	} else {
		for _, dayA := range a.daysOfWeek {
			for _, dayB := range b.daysOfWeek {
				if dayA == dayB {
					shareDay = true
					break
				}
			}
			if shareDay {
				break
			}
		}
	}
	if !shareDay {
		return false
	}
	if a.endClockMinutes+bufferMinutesBetweenSessions <= b.startClockMinutes ||
		b.endClockMinutes+bufferMinutesBetweenSessions <= a.startClockMinutes {
		return false
	}
	return true
}

func loadWantFileData(csvBytes []byte) wantFileData {
	csvReader := csv.NewReader(bytes.NewReader(csvBytes))
	headerRow, _ := csvReader.Read()

	type columnIndices struct{ age, priority int }
	indicesByChild := map[string]*columnIndices{}

	for index, headerValue := range headerRow {
		headerLower := strings.ToLower(headerValue)
		switch {
		case strings.Contains(headerLower, "'s age"):
			child := strings.Trim(strings.Split(headerValue, "'")[0], `" `)
			if indicesByChild[child] == nil {
				indicesByChild[child] = &columnIndices{age: -1, priority: -1}
			}
			indicesByChild[child].age = index
		case strings.Contains(headerLower, "'s priority"):
			child := strings.Trim(strings.Split(headerValue, "'")[0], `" `)
			if indicesByChild[child] == nil {
				indicesByChild[child] = &columnIndices{age: -1, priority: -1}
			}
			indicesByChild[child].priority = index
		}
	}

	childAges := map[string]int{}
	priorityBySession := map[string]map[string]string{}

	for {
		row, readError := csvReader.Read()
		if readError != nil {
			break
		}
		if len(row) == 0 {
			continue
		}
		sessionName := strings.TrimSpace(row[0])
		for child, indices := range indicesByChild {
			if indices.age >= 0 && indices.age < len(row) && childAges[child] == 0 {
				if parsedAge, err := strconv.Atoi(strings.TrimSpace(row[indices.age])); err == nil {
					childAges[child] = parsedAge
				}
			}
			priorityValue := priorityNoLiteral
			if indices.priority >= 0 && indices.priority < len(row) {
				if text := strings.TrimSpace(row[indices.priority]); text != "" {
					priorityValue = text
				}
			}
			if priorityBySession[sessionName] == nil {
				priorityBySession[sessionName] = map[string]string{}
			}
			priorityBySession[sessionName][child] = priorityValue
		}
	}

	var childNames []string
	for child := range childAges {
		childNames = append(childNames, child)
	}
	sort.Strings(childNames)

	return wantFileData{
		childAgesByName:        childAges,
		childNamesSorted:       childNames,
		sessionPriorityByChild: priorityBySession,
	}
}
