package iphonemodel

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMutateScenarioIsDeterministic(t *testing.T) {
	base, err := BuiltInScenario("portrait-video-to-landscape", 8*time.Second, 9)
	if err != nil {
		t.Fatal(err)
	}
	a, mutationA, err := MutateScenario(base, 77)
	if err != nil {
		t.Fatal(err)
	}
	b, mutationB, err := MutateScenario(base, 77)
	if err != nil {
		t.Fatal(err)
	}
	jsonA, _ := json.Marshal(struct {
		Scenario Scenario `json:"scenario"`
		Mutation Mutation `json:"mutation"`
	}{a, mutationA})
	jsonB, _ := json.Marshal(struct {
		Scenario Scenario `json:"scenario"`
		Mutation Mutation `json:"mutation"`
	}{b, mutationB})
	if string(jsonA) != string(jsonB) {
		t.Fatalf("same seed differed:\n%s\n%s", jsonA, jsonB)
	}
	if mutationA.Name == "" || mutationA.Event < 0 || mutationA.After == mutationA.Before {
		t.Fatalf("invalid mutation metadata: %#v", mutationA)
	}
	if err := ValidateScenario(a); err != nil {
		t.Fatalf("mutation produced invalid scenario: %v", err)
	}
}

func TestMutateScenarioDoesNotChangeInput(t *testing.T) {
	base, err := BuiltInScenario("steady-60", 8*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(base)
	if _, _, err := MutateScenario(base, 2); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(base)
	if string(before) != string(after) {
		t.Fatalf("input mutated:\n%s\n%s", before, after)
	}
}
