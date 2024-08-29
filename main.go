package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

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
}

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

	results := 30
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

	// Check if the new test is the first child of an existing test.
	// If it is, stop the execution time of the parent test.
	for _, test := range runningTests {
		if strings.HasPrefix(event.Test, test.Name+"/") {
			allTests[event.Test].Parent = test
			test.Children = append(test.Children, allTests[event.Test])
			if len(test.Children) == 1 {
				updateExecutionTimes(test, event)
				// Stop the execution time of the parent test -- remove parent from running tests
				delete(runningTests, test.Name)
			} else {
				// Once a child starts up, we should have removed the parent from running tests
				panic("Running parent test has multiple running children: " + test.Name)
			}
			runningTests[event.Test] = allTests[event.Test]
			return
		}
	}

	// Update running test durations and add the new test to the list of running tests
	for _, runningTest := range runningTests {
		updateExecutionTimes(runningTest, event)
	}
	runningTests[event.Test] = allTests[event.Test]
}

func handlePause(event Event) {
	_, ok := runningTests[event.Test]
	if !ok {
		fmt.Printf("WARNING: Paused test not found in running tests: %s\n", event.Test)
		return
	}

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
	runningTests[event.Test] = test
}

func updateRunningTests(event Event) {
	for _, runningTest := range runningTests {
		updateExecutionTimes(runningTest, event)
	}
}

func updateExecutionTimes(runningTest *RunningTest, event Event) {
	runningTest.AdjustedExecutionTime += event.Time.Sub(runningTest.LastTimestamp) / time.Duration(len(runningTests))
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
			// If there are still other children executing, update the durations of currently running tests
			updateRunningTests(event)
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
