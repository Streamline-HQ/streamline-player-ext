package access

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAccess(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Access Suite")
}
