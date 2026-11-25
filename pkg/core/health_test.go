package core

import "testing"

func TestHealthcheck(t *testing.T) {
	if Healthcheck() != "ok" {
		t.Fatal("unexpected")
	}
}
