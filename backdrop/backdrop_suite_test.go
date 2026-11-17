package backdrop_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBackdrop(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backdrop Suite")
}
