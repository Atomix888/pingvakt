package main

import (
	"os"
	"testing"
)

// Survey-vyn delas mellan servern, skrivbordsappen och Android-appen och måste vara identisk.
func TestSharedSurveyUI(t *testing.T) {
	a, err := os.ReadFile("web/surveyui.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../desktop/frontend/dist/surveyui.js", "../agent/mobileui/surveyui.js"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("%s skiljer sig från server/web/surveyui.js", p)
		}
	}
}
