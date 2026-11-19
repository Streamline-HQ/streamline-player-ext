package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWatchImport(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Watch Import Suite")
}
