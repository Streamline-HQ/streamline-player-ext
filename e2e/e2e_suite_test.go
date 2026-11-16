package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

// stack is the one Streamline every spec of this process talks to, booted
// once with player.wasm granted exactly what its manifest asks for.
var stack *harness.Stack

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Player Extension E2E Suite")
}

var _ = BeforeSuite(func() {
	stack = harness.Boot(harness.Options{
		Library: "testdata/library.json",
		Engine:  true,
		Log:     GinkgoWriter,
	})
	DeferCleanup(stack.Close)
})
