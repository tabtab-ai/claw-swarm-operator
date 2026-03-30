package claw

import (
	"errors"
	"testing"
)

func checkResult(s string, length int) error {
	if len(s) != length {
		return errors.New("length mismatch")
	}
	if s[0] >= '0' && s[0] <= '9' {
		return errors.New("start with number")
	}

	return nil
}

func TestRandomName(t *testing.T) {
	lengths := []int{
		2, 8,
	}
	for i := range lengths {
		got, err := RandomName(lengths[i])
		if err != nil {
			t.Fatal(err.Error())
		}
		if err := checkResult(got, lengths[i]); err != nil {
			t.Fatal(err.Error())
		}
	}

	length := 0
	_, err := RandomName(length)
	if err == nil {
		t.Fatalf("length must be positive")
	}
}
