package sign

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSign(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Sign Suite")
}
