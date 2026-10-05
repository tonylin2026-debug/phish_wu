package models

import (
	"fmt"

	check "gopkg.in/check.v1"
)

type mockTemplateContext struct {
	URL         string
	FromAddress string
}

func (m mockTemplateContext) getFromAddress() string {
	return m.FromAddress
}

func (m mockTemplateContext) getBaseURL() string {
	return m.URL
}

func (s *ModelsSuite) TestNewTemplateContext(c *check.C) {
	r := Result{
		BaseRecipient: BaseRecipient{
			FirstName: "Foo",
			LastName:  "Bar",
			Email:     "foo@bar.com",
		},
		RId: "1234567",
	}
	ctx := mockTemplateContext{
		URL:         "http://example.com",
		FromAddress: "From Address <from@example.com>",
	}
	expected := PhishingTemplateContext{
		URL:                   fmt.Sprintf("%s?rid=%s", ctx.URL, r.RId),
		BaseURL:               ctx.URL,
		BaseRecipient:         r.BaseRecipient,
		TrackingURL:           fmt.Sprintf("%s/track?rid=%s", ctx.URL, r.RId),
		AttachmentTrackingURL: fmt.Sprintf("%s/track/attachment?rid=%s", ctx.URL, r.RId),
		From:                  "From Address",
		RId:                   r.RId,
	}
	expected.Tracker = "<img alt='' style='display: none' src='" + expected.TrackingURL + "'/>"
	got, err := NewPhishingTemplateContext(ctx, r.BaseRecipient, r.RId)
	c.Assert(err, check.Equals, nil)
	c.Assert(got, check.DeepEquals, expected)
}

func (s *ModelsSuite) TestNewTemplateContextWithPath(c *check.C) {
	r := Result{BaseRecipient: BaseRecipient{Email: "foo@bar.com"}, RId: "abcdef1"}
	ctx := mockTemplateContext{
		URL:         "https://example.com/training/landing",
		FromAddress: "from@example.com",
	}
	got, err := NewPhishingTemplateContext(ctx, r.BaseRecipient, r.RId)
	c.Assert(err, check.IsNil)
	c.Assert(got.TrackingURL, check.Equals, "https://example.com/training/landing/track?rid=abcdef1")
	c.Assert(got.AttachmentTrackingURL, check.Equals, "https://example.com/training/landing/track/attachment?rid=abcdef1")
}
