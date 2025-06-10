package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/or-tools/sat"
)

/* Session is one sub-activity row extracted from sessions.json. */
type Session struct {
	ID           int             `json:"id"`
	ActivityName string          `json:"title"`
	DateRange    string          `json:"dateRange"`
	DayAndTime   string          `json:"dayTime"`
	SeatsOpen    string          `json:"seatsOpen"`
	ChildAllowed map[string]bool `json:"childAllowed"` // {"Alice":true, "Peter":false}
	StartTime    time.Time       `json:"-"`
	EndTime      time.Time       `json:"-"`
	ActivityDate time.Time       `json:"-"`
}

/* loadSessions reads the scraper output and normalises the time fields. */
func loadSessions(jsonFile string) []Session {
	rawBytes, err := os.ReadFile(jsonFile)
	if err != nil {
		log.Fatalf("read %s: %v", jsonFile, err)
	}

	var parsed []Session
	if err := json.Unmarshal(rawBytes, &parsed); err != nil {
		log.Fatalf("json parse: %v", err)
	}

	for index, sess := range parsed {
		// example: "Jun 10, 2025 to Jul 1, 2025"  → we use the first date
		dateField := sess.DateRange
		if comma := len(dateField); comma > 0 {
			dateField = dateField[:10]
		}
		sessionDate, _ := time.Parse("Jan 2 2006", dateField)
		startClock, endClock := parseClockRange(sess.DayAndTime)

		parsed[index].ActivityDate = sessionDate
		parsed[index].StartTime = sessionDate.Add(startClock)
		parsed[index].EndTime = sessionDate.Add(endClock)
	}

	return parsed
}

/* parseClockRange converts "Tue 4:30 PM – 5:00 PM" into offsets since midnight. */
func parseClockRange(text string) (time.Duration, time.Duration) {
	var weekday string
	var startString string
	var endString string
	fmt.Sscanf(text, "%s %s – %s", &weekday, &startString, &endString)

	toDuration := func(clock string) time.Duration {
		t, _ := time.Parse("3:04PM", clock)
		return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	}
	return toDuration(startString), toDuration(endString)
}

/* sameDayAndOverlap applies the 30-minute travel buffer. */
func sameDayAndOverlap(first, second Session, buffer time.Duration) bool {
	if !first.ActivityDate.Equal(second.ActivityDate) {
		return false
	}
	aStarts := first.StartTime
	aEnds := first.EndTime.Add(buffer)
	bStarts := second.StartTime
	bEnds := second.EndTime.Add(buffer)
	return aStarts.Before(bEnds) && bStarts.Before(aEnds)
}

func main() {
	const jsonInput = "sessions.json"
	sessionList := loadSessions(jsonInput)

	optimisationModel := sat.NewCpModel()
	decisionVar := map[string]*sat.BoolVar{}

	for _, session := range sessionList {
		for childName := range session.ChildAllowed {
			key := fmt.Sprintf("%s|%d", childName, session.ID)
			decisionVar[key] = optimisationModel.NewBoolVar(key)
		}
	}

	/* 1. one session per activity per child */
	for _, childName := range []string{"Alice", "Peter"} {
		groupByActivity := map[string][]*sat.BoolVar{}
		for _, session := range sessionList {
			if !session.ChildAllowed[childName] {
				continue
			}
			groupByActivity[session.ActivityName] =
				append(groupByActivity[session.ActivityName],
					decisionVar[fmt.Sprintf("%s|%d", childName, session.ID)])
		}
		for _, varSlice := range groupByActivity {
			optimisationModel.AddLinearConstraint(varSlice, 0, 1)
		}
	}

	/* 2. no overlaps per child */
	travelBuffer := 30 * time.Minute
	for _, childName := range []string{"Alice", "Peter"} {
		for firstIndex, firstSession := range sessionList {
			if !firstSession.ChildAllowed[childName] {
				continue
			}
			for _, secondSession := range sessionList[firstIndex+1:] {
				if !secondSession.ChildAllowed[childName] {
					continue
				}
				if sameDayAndOverlap(firstSession, secondSession, travelBuffer) {
					optimisationModel.AddLinearConstraint(
						[]*sat.BoolVar{
							decisionVar[fmt.Sprintf("%s|%d", childName, firstSession.ID)],
							decisionVar[fmt.Sprintf("%s|%d", childName, secondSession.ID)],
						},
						0, 1)
				}
			}
		}
	}

	/* 3. maximise the number of attended sessions */
	objective := optimisationModel.NewLinearExpr()
	for _, variable := range decisionVar {
		objective.AddTerm(variable, 1)
	}
	optimisationModel.Maximise(objective)

	solver := sat.NewCpSolver()
	if solver.Solve(optimisationModel) != sat.Optimal {
		log.Fatal("no feasible schedule")
	}

	printSchedule(sessionList, decisionVar, solver)
}

/* printSchedule gives the plain-text table you asked for. */
func printSchedule(sessionList []Session, decision map[string]*sat.BoolVar, solver *sat.CpSolver) {
	fmt.Printf("| %-11s | %-35s | %-35s |\n", "Date", "Alice", "Peter")
	fmt.Println("|-------------|-------------------------------------|-------------------------------------|")

	for _, session := range sessionList {
		aliceChosen := solver.BooleanValue(decision["Alice|"+fmt.Sprint(session.ID)])
		peterChosen := solver.BooleanValue(decision["Peter|"+fmt.Sprint(session.ID)])

		if !aliceChosen && !peterChosen {
			continue
		}

		rowDate := session.ActivityDate.Format("Mon 02 Jan")
		aliceCell := ""
		peterCell := ""

		if aliceChosen {
			aliceCell = fmt.Sprintf("%s %s", session.ActivityName, session.DayAndTime)
		}
		if peterChosen {
			peterCell = fmt.Sprintf("%s %s", session.ActivityName, session.DayAndTime)
		}

		fmt.Printf("| %-11s | %-35s | %-35s |\n", rowDate, aliceCell, peterCell)
	}
}
