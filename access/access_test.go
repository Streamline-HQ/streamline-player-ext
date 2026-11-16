package access

import (
	"net/http"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("the access decision", g.Label("unit"), func() {
	decide := func(l Level, c Cred, role string) (int, string) {
		return Decide(l, c, role)
	}

	g.It("lets a public route through without a credential", func() {
		s, c := decide(Public, None, "")
		Expect(s).To(Equal(http.StatusOK))
		Expect(c).To(BeEmpty())
	})

	g.It("answers 401 to no credential on every other route", func() {
		for _, l := range []Level{Any, BearerOnly, DeviceOnly, Admin} {
			s, c := decide(l, None, "")
			Expect(s).To(Equal(http.StatusUnauthorized))
			Expect(c).To(Equal("unauthorized"))
		}
	})

	g.It("tells a device key it needs a browser session", func() {
		s, c := decide(BearerOnly, Key, "admin")
		Expect(s).To(Equal(http.StatusForbidden))
		Expect(c).To(Equal("session_required"))
	})

	g.It("refuses a bearer on a device-only route", func() {
		s, c := decide(DeviceOnly, Bearer, "admin")
		Expect(s).To(Equal(http.StatusForbidden))
		Expect(c).To(Equal("forbidden"))
	})

	g.It("refuses a non-admin on an admin route with either credential", func() {
		for _, cred := range []Cred{Key, Bearer} {
			s, c := decide(Admin, cred, "user")
			Expect(s).To(Equal(http.StatusForbidden))
			Expect(c).To(Equal("forbidden"))
		}
	})

	g.It("serves what each level allows", func() {
		for _, tc := range []struct {
			l    Level
			c    Cred
			role string
		}{
			{Any, Key, "user"},
			{Any, Bearer, "user"},
			{BearerOnly, Bearer, "user"},
			{DeviceOnly, Key, "user"},
			{Admin, Key, "admin"},
			{Admin, Bearer, "admin"},
		} {
			s, c := decide(tc.l, tc.c, tc.role)
			Expect(s).To(Equal(http.StatusOK), "%v %v", tc.l, tc.c)
			Expect(c).To(BeEmpty())
		}
	})

	g.It("fails closed on a level it does not know", func() {
		s, _ := decide(Level(99), Key, "admin")
		Expect(s).To(Equal(http.StatusForbidden))
	})
})
