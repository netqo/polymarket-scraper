// Test data: Invented data. Log categories are this program's own vocabulary.

package config

import (
	"slices"
	"strings"
	"testing"
)

// A run that quietly says nothing about a whole class of problem is worse than
// a noisy one, so everything is on until someone says otherwise.
func TestEveryCategoryIsOnByDefault(t *testing.T) {
	got, err := Parse(minimalArgs(), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if want := AllLogCategories(); got.LogCategories != want {
		t.Errorf("LogCategories = %+v, want %+v", got.LogCategories, want)
	}
	if off := got.LogCategories.Disabled(); len(off) != 0 {
		t.Errorf("Disabled() = %v, want nothing switched off", off)
	}
}

func TestCategoriesCanBeSwitchedOffIndividually(t *testing.T) {
	path := writeConfig(t, `
[logging.categories]
flags      = false
connection = false
`)

	got, err := Parse(minimalArgs("--config", path), noEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	off := got.LogCategories.Disabled()
	slices.Sort(off)

	want := []string{"connection", "flags"}
	if !slices.Equal(off, want) {
		t.Errorf("Disabled() = %v, want %v", off, want)
	}

	// The ones not mentioned are untouched.
	if !got.LogCategories.Startup || !got.LogCategories.REST || !got.LogCategories.Discovery {
		t.Errorf("LogCategories = %+v, want the unmentioned switches left on", got.LogCategories)
	}
}

func TestDisabledNamesMatchWhatTheLoggingPackageExpects(t *testing.T) {
	// Spelled out rather than derived, so that renaming a category in one
	// package and not the other fails here rather than silently switching
	// nothing off.
	all := LogCategories{}

	off := all.Disabled()
	slices.Sort(off)

	want := []string{"connection", "decode", "discovery", "flags", "progress", "rest", "startup"}
	if !slices.Equal(off, want) {
		t.Errorf("Disabled() with everything off = %v, want %v", off, want)
	}
}

// A misspelled category has to be refused like any other unknown setting, or
// switching one off would silently do nothing.
func TestAnUnknownCategoryIsRejected(t *testing.T) {
	path := writeConfig(t, "[logging.categories]\nflagz = false\n")

	_, err := Parse(minimalArgs("--config", path), noEnv)
	if err == nil {
		t.Fatal("Parse accepted an unknown category")
	}
	if !strings.Contains(err.Error(), "flagz") {
		t.Errorf("error %q does not name the offending category", err)
	}
}
