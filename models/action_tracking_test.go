package models

import (
	"bytes"
	"encoding/base64"
	"strings"
	"sync"

	"github.com/gophish/gomail"
	"github.com/jordan-wright/email"
	check "gopkg.in/check.v1"
)

// applyAction runs one of the three tracked actions against a result.
func applyAction(c *check.C, r *Result, action string) {
	switch action {
	case "open":
		c.Assert(r.HandleEmailOpened(EventDetails{}), check.IsNil)
	case "attachment":
		c.Assert(r.HandleAttachmentOpened(EventDetails{}), check.IsNil)
	case "click":
		c.Assert(r.HandleClickedLink(EventDetails{}), check.IsNil)
	default:
		c.Fatalf("unknown action %q", action)
	}
}

// TestThreeActionsAreIndependent walks every ordering of the three tracked
// actions and asserts all three flags survive regardless of which arrived
// first.
//
// This was impossible before independent flags existed: Status is an ordered
// state machine holding a single value, so recording one action necessarily
// discarded or suppressed another.
func (s *ModelsSuite) TestThreeActionsAreIndependent(c *check.C) {
	orders := [][]string{
		{"open", "attachment", "click"},
		{"open", "click", "attachment"},
		{"click", "open", "attachment"},
		{"click", "attachment", "open"},
		{"attachment", "open", "click"},
		{"attachment", "click", "open"},
	}
	for _, order := range orders {
		comment := check.Commentf("action order %v", order)
		campaign := s.createCampaign(c)
		result := campaign.Results[0]

		for _, action := range order {
			applyAction(c, &result, action)
		}

		stored, err := GetResult(result.RId)
		c.Assert(err, check.IsNil, comment)
		c.Assert(stored.EmailOpened, check.Equals, true, comment)
		c.Assert(stored.AttachmentOpened, check.Equals, true, comment)
		c.Assert(stored.ClickedLink, check.Equals, true, comment)
		c.Assert(stored.SubmittedData, check.Equals, false, comment)
		// Status keeps reporting the furthest step in the ordered progression,
		// which is Clicked Link no matter what order the actions arrived in.
		c.Assert(stored.Status, check.Equals, EventClicked, comment)

		s.TearDownTest(c)
	}
}

// TestConcurrentActionsDoNotClobberEachOther is the regression test for the
// lost update that independent flags alone would not have fixed.
//
// The three actions arrive as three separate HTTP requests which can overlap.
// Each handler used to load the row, mutate its own copy and write every
// column back with db.Save(), so the last writer silently erased flags set by
// the others. The handlers now issue targeted UPDATE statements instead.
func (s *ModelsSuite) TestConcurrentActionsDoNotClobberEachOther(c *check.C) {
	campaign := s.createCampaign(c)
	rid := campaign.Results[0].RId

	actions := []string{"open", "attachment", "click"}
	errs := make(chan error, len(actions))
	var wg sync.WaitGroup

	for _, action := range actions {
		wg.Add(1)
		go func(action string) {
			defer wg.Done()
			// Each goroutine loads its own snapshot, exactly as three
			// concurrent requests to the phishing server would.
			r, err := GetResult(rid)
			if err != nil {
				errs <- err
				return
			}
			switch action {
			case "open":
				errs <- r.HandleEmailOpened(EventDetails{})
			case "attachment":
				errs <- r.HandleAttachmentOpened(EventDetails{})
			case "click":
				errs <- r.HandleClickedLink(EventDetails{})
			}
		}(action)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		c.Assert(err, check.IsNil)
	}

	stored, err := GetResult(rid)
	c.Assert(err, check.IsNil)
	c.Assert(stored.EmailOpened, check.Equals, true)
	c.Assert(stored.AttachmentOpened, check.Equals, true)
	c.Assert(stored.ClickedLink, check.Equals, true)
}

// TestAttachmentOpenDoesNotImplyEmailOpen pins down the separation in the
// other direction: a recipient whose mail client blocked the tracking pixel
// but who still opened the attachment must not be counted as having opened
// the email, and the ordered Status must stay untouched.
func (s *ModelsSuite) TestAttachmentOpenDoesNotImplyEmailOpen(c *check.C) {
	campaign := s.createCampaign(c)
	result := campaign.Results[0]
	c.Assert(result.HandleAttachmentOpened(EventDetails{}), check.IsNil)

	stored, err := GetResult(result.RId)
	c.Assert(err, check.IsNil)
	c.Assert(stored.AttachmentOpened, check.Equals, true)
	c.Assert(stored.EmailOpened, check.Equals, false)
	c.Assert(stored.ClickedLink, check.Equals, false)
	c.Assert(stored.Status, check.Equals, StatusSending)
	// It still counts as engagement for the "reached" figure.
	c.Assert(stored.Engaged(), check.Equals, true)
}

// TestRepeatedActionsAreIdempotent covers the common case of a document being
// opened several times, or a mail client prefetching the pixel more than once.
func (s *ModelsSuite) TestRepeatedActionsAreIdempotent(c *check.C) {
	campaign := s.createCampaign(c)
	result := campaign.Results[0]
	for i := 0; i < 3; i++ {
		c.Assert(result.HandleAttachmentOpened(EventDetails{}), check.IsNil)
		c.Assert(result.HandleEmailOpened(EventDetails{}), check.IsNil)
	}
	stored, err := GetResult(result.RId)
	c.Assert(err, check.IsNil)
	c.Assert(stored.AttachmentOpened, check.Equals, true)
	c.Assert(stored.EmailOpened, check.Equals, true)
	c.Assert(stored.Status, check.Equals, EventOpened)
}

// TestFormSubmitImpliesClick records that submitting the landing page form
// necessarily means the link was followed, even when the click itself was
// never separately observed.
func (s *ModelsSuite) TestFormSubmitImpliesClick(c *check.C) {
	campaign := s.createCampaign(c)
	result := campaign.Results[0]
	c.Assert(result.HandleFormSubmit(EventDetails{}), check.IsNil)

	stored, err := GetResult(result.RId)
	c.Assert(err, check.IsNil)
	c.Assert(stored.SubmittedData, check.Equals, true)
	c.Assert(stored.ClickedLink, check.Equals, true)
	c.Assert(stored.EmailOpened, check.Equals, false)
	c.Assert(stored.Status, check.Equals, EventDataSubmit)
}

// TestCampaignStatsCountActionsIndependently checks the numbers the UI and the
// API report, including the two-column opened figure: OpenedEmail is what the
// tracking pixel actually measured, EngagedEmail is everyone who interacted at
// all.
func (s *ModelsSuite) TestCampaignStatsCountActionsIndependently(c *check.C) {
	campaign := s.createCampaign(c)
	c.Assert(len(campaign.Results) >= 4, check.Equals, true)

	attachmentOnly := campaign.Results[0]
	clickOnly := campaign.Results[1]
	allThree := campaign.Results[2]
	// campaign.Results[3] does nothing at all.

	c.Assert(attachmentOnly.HandleAttachmentOpened(EventDetails{}), check.IsNil)
	c.Assert(clickOnly.HandleClickedLink(EventDetails{}), check.IsNil)
	c.Assert(allThree.HandleEmailOpened(EventDetails{}), check.IsNil)
	c.Assert(allThree.HandleAttachmentOpened(EventDetails{}), check.IsNil)
	c.Assert(allThree.HandleClickedLink(EventDetails{}), check.IsNil)

	stats, err := getCampaignStats(campaign.Id)
	c.Assert(err, check.IsNil)
	c.Assert(stats.Total, check.Equals, int64(len(campaign.Results)))
	// Measured: only the recipient whose pixel actually loaded.
	c.Assert(stats.OpenedEmail, check.Equals, int64(1))
	c.Assert(stats.AttachmentOpened, check.Equals, int64(2))
	c.Assert(stats.ClickedLink, check.Equals, int64(2))
	c.Assert(stats.SubmittedData, check.Equals, int64(0))
	// Reached: everyone who did anything, regardless of image blocking.
	c.Assert(stats.EngagedEmail, check.Equals, int64(3))
	c.Assert(stats.EmailReported, check.Equals, int64(0))
}

// decodeAttachment returns the usable body of an attachment parsed back out of
// a generated message.
//
// jordan-wright/email does not decode the Content-Transfer-Encoding of
// attachment parts, so Attachment.Content is still base64 and wrapped at 76
// columns. Anything that wants to assert on the rendered text has to undo that
// first.
func decodeAttachment(raw []byte) string {
	// strings.Fields drops the \r\n wrapping that base64 line folding adds.
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if err != nil {
		// Not base64 after all - hand back what we were given.
		return string(raw)
	}
	return string(decoded)
}

// TestAttachmentTrackerTagUsesAttachmentEndpoint guards the other way the two
// actions could interfere: {{.Tracker}} is a pre-rendered <img> tag built from
// TrackingURL, so without an explicit override an attachment using it would
// call /track and be misreported as an email open.
func (s *ModelsSuite) TestAttachmentTrackerTagUsesAttachmentEndpoint(c *check.C) {
	ptx := PhishingTemplateContext{
		TrackingURL:           "https://example.com/track?rid=abc1234",
		AttachmentTrackingURL: "https://example.com/track/attachment?rid=abc1234",
	}
	ptx.Tracker = "<img alt='' style='display: none' src='" + ptx.TrackingURL + "'/>"

	a := Attachment{
		Name:    "tracking.html",
		Type:    "text/html",
		Content: base64.StdEncoding.EncodeToString([]byte("tracker={{.Tracker}}")),
	}

	msg := gomail.NewMessage()
	addAttachment(msg, a, ptx)

	msgBuff := &bytes.Buffer{}
	_, err := msg.WriteTo(msgBuff)
	c.Assert(err, check.IsNil)
	got, err := email.NewEmailFromReader(msgBuff)
	c.Assert(err, check.IsNil)
	c.Assert(got.Attachments, check.HasLen, 1)

	rendered := decodeAttachment(got.Attachments[0].Content)
	c.Assert(rendered, check.Equals,
		"tracker=<img alt='' style='display: none' src='"+ptx.AttachmentTrackingURL+"'/>")
	c.Assert(strings.Contains(rendered, ptx.AttachmentTrackingURL), check.Equals, true)
	c.Assert(strings.Contains(rendered, "src='https://example.com/track?rid=abc1234'"), check.Equals, false)

	// The caller's context must not have been mutated - the email body still
	// needs the original /track pixel.
	c.Assert(ptx.TrackingURL, check.Equals, "https://example.com/track?rid=abc1234")
	c.Assert(strings.Contains(ptx.Tracker, "/track?rid=abc1234"), check.Equals, true)
}
