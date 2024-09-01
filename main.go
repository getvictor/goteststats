package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const resultsToList = 50

type Event struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Test    string    `json:"Test"`
	Package string    `json:"Package"`
}

type RunningTest struct {
	Name                  string
	Package               string
	LastTimestamp         time.Time
	AdjustedExecutionTime time.Duration
	TotalExecutionTime    time.Duration
	Children              []*RunningTest
	Parent                *RunningTest
	AssumedStopped        bool
	Parallel              bool
}

var subTestRegexp = regexp.MustCompile("^(?P<parent>\\S+)/\\S+$")

// Pre-allocate some memory for the tests
var allTests = make(map[string]*RunningTest, 1000)
var runningTests = make(map[string]*RunningTest, 10)

func main() {

	reader := bufio.NewReader(os.Stdin)

	for {
		exitLoop := false
		line, err := reader.ReadBytes('\n')
		switch {
		case err == io.EOF:
			exitLoop = true
		case err != nil:
			panic(err)
		}
		if exitLoop {
			break
		}
		var event Event
		err = json.Unmarshal(line, &event)
		if err != nil {
			panic(err)
		}
		// Ignore events without a test -- ignore package events
		if event.Test == "" {
			continue
		}
		switch event.Action {
		case "run":
			handleRun(event)
		case "pause":
			handlePause(event)
		case "cont":
			handleCont(event)
		case "pass", "skip":
			handleStop(event)
		case "output", "start":
			continue
		default:
			panic("Unknown action: " + event.Action)
		}
	}

	for _, runningTest := range runningTests {
		fmt.Printf("WARNING: Test %s is still running\n", runningTest.Name)
	}

	// Print the results
	keys := make([]string, 0, len(allTests))
	for k := range allTests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return allTests[keys[i]].AdjustedExecutionTime > allTests[keys[j]].AdjustedExecutionTime
	})

	results := resultsToList
	if len(keys) < results {
		results = len(keys)
	}
	for i := 0; i < results; i++ {
		test := allTests[keys[i]]
		adjustedRounded := test.AdjustedExecutionTime.Round(time.Millisecond)
		totalRounded := test.TotalExecutionTime.Round(time.Millisecond)
		if adjustedRounded != totalRounded {
			fmt.Printf("%s %s: %s (total: %s parallel: %d)\n", test.Package, test.Name, adjustedRounded, totalRounded,
				totalRounded/adjustedRounded)
		} else {
			fmt.Printf("%s %s: %s\n", test.Package, test.Name, adjustedRounded)
		}
	}

}

func handleRun(event Event) {
	allTests[event.Test] = &RunningTest{
		Name:          event.Test,
		Package:       event.Package,
		LastTimestamp: event.Time,
	}

	parent, subtest := isSubTest(event.Test)

	if subtest {
		_, ok := allTests[parent]
		if !ok {
			// Parent for subtest must exist. If it doesn't, we go through all tests and find the parent, which is the longest string that is a prefix of the subtest.
			var names []string
			for test := range allTests {
				names = append(names, test)
			}
			// Sort names by length in descending order
			sort.Slice(names, func(i, j int) bool {
				l1, l2 := len(names[i]), len(names[j])
				return l1 > l2
			})
			for _, name := range names {
				if strings.HasPrefix(event.Test, name+"/") {
					parent = name
					break
				}
			}
			if parent == "" {
				panic("Parent test not found for subtest: " + event.Test)
			}
		}
	}

	if subtest {
		// Check if the new subtest test is the first child of an existing test.
		// If it is, stop the execution time of the parent test.
		runningParent, ok := runningTests[parent]
		if ok {
			allTests[event.Test].Parent = runningParent
			runningParent.Children = append(runningParent.Children, allTests[event.Test])
			if len(runningParent.Children) == 1 {
				updateExecutionTimes(runningParent, event)
				// Stop the execution time of the parent test -- remove parent from running tests
				delete(runningTests, runningParent.Name)
			} else {
				// Once a child starts up, we should have removed the parent from running tests
				panic("Running parent test has multiple running children: " + runningParent.Name)
			}
			runningTests[event.Test] = allTests[event.Test]
			return
		}

		// Check if the new subtest has currently running siblings. If so, we assume those siblings stop, since subtests run in series by default.
		for _, runningTest := range runningTests {
			if stopSibling(event, runningTest, runningTest, parent) {
				return
			}
		}

		// Otherwise, this is probably a sibling of a parallel test which is currently paused.
	}

	// Update running test durations and add the new test to the list of running tests
	updateRunningTests(event)
	runningTests[event.Test] = allTests[event.Test]
}

func stopSibling(event Event, runningTest *RunningTest, potentialSibling *RunningTest, parent string) bool {
	if potentialSibling.Parent != nil && potentialSibling.Parent.Name == parent && !runningTest.Parallel && !runningTest.AssumedStopped {

		updateExecutionTimes(runningTest, event)
		// This means that the test is actually finished, but its result had not been reported yet
		runningTest.AssumedStopped = true
		allTests[event.Test].Parent = potentialSibling.Parent
		potentialSibling.Parent.Children = append(potentialSibling.Parent.Children, allTests[event.Test])
		runningTests[event.Test] = allTests[event.Test]
		// One test swapped for another -- no need to update running times for all tests
		return true
	}
	// Check for children of this sibling.
	if potentialSibling.Parent != nil && potentialSibling.Parent.Parent != nil {
		return stopSibling(event, runningTest, potentialSibling.Parent, parent)
	}
	return false
}

func handlePause(event Event) {
	pausedTest, ok := runningTests[event.Test]
	if !ok {
		fmt.Printf("WARNING: Paused test not found in running tests: %s\n", event.Test)
		return
	}

	// If test was paused, we assume it was paused due to t.Parallel call
	pausedTest.Parallel = true
	pausedTest.AssumedStopped = false
	updateRunningTests(event)
	delete(runningTests, event.Test)
}

func handleCont(event Event) {
	test, ok := allTests[event.Test]
	if !ok {
		fmt.Printf("WARNING: Continued test not found in tests: %s\n", event.Test)
		return
	}

	// Update running test durations and add the new test to the list of running tests
	for _, runningTest := range runningTests {
		updateExecutionTimes(runningTest, event)
	}

	test.LastTimestamp = event.Time
	test.AssumedStopped = false
	runningTests[event.Test] = test
}

func updateRunningTests(event Event) {
	var count uint64
	for _, runningTest := range runningTests {
		if !runningTest.AssumedStopped {
			count++
		}
	}
	for _, runningTest := range runningTests {
		updateExecutionTimesWithCount(runningTest, event, count)
	}
}

func updateExecutionTimes(runningTest *RunningTest, event Event) {
	if runningTest.AssumedStopped {
		return
	}
	var count uint64
	for _, test := range runningTests {
		if !test.AssumedStopped {
			count++
		}
	}
	runningTest.AdjustedExecutionTime += event.Time.Sub(runningTest.LastTimestamp) / time.Duration(count)
	runningTest.TotalExecutionTime += event.Time.Sub(runningTest.LastTimestamp)
	runningTest.LastTimestamp = event.Time
}

func updateExecutionTimesWithCount(runningTest *RunningTest, event Event, count uint64) {
	if runningTest.AssumedStopped {
		return
	}
	runningTest.AdjustedExecutionTime += event.Time.Sub(runningTest.LastTimestamp) / time.Duration(count)
	runningTest.TotalExecutionTime += event.Time.Sub(runningTest.LastTimestamp)
	runningTest.LastTimestamp = event.Time
}

func handleStop(event Event) {
	test, ok := runningTests[event.Test]
	if !ok {
		fmt.Printf("WARNING: Stopped test not found in running tests: %s\n", event.Test)
		return
	}

	if test.Parent != nil {
		if len(test.Parent.Children) == 0 {
			panic("Parent test has no children: " + test.Parent.Name)
		} else if len(test.Parent.Children) == 1 {
			// If this is the last executing child of parent, restart the execution time of the parent test
			updateExecutionTimes(test, event)
			test.Parent.Children = nil
			runningTests[test.Parent.Name] = test.Parent
			runningTests[test.Parent.Name].LastTimestamp = event.Time
		} else {
			if !test.AssumedStopped {
				// If there are still other children executing, update the durations of currently running tests
				updateRunningTests(event)
			}
			// Remove the child from parent
			for i, child := range test.Parent.Children {
				if child == test {
					test.Parent.Children = append(test.Parent.Children[:i], test.Parent.Children[i+1:]...)
					break
				}
			}
		}
		delete(runningTests, event.Test)
		return
	}

	updateRunningTests(event)
	delete(runningTests, event.Test)
}

func isSubTest(test string) (string, bool) {
	match := subTestRegexp.FindStringSubmatch(test)
	if len(match) == 0 {
		return "", false
	}

	var parent string
	for i, name := range subTestRegexp.SubexpNames() {
		if i > 0 && i <= len(match) && name == "parent" {
			parent = match[i]
			break
		}
	}
	return parent, true
}
