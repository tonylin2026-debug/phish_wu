package controllers

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/gophish/gophish/models"
)

// fireAction performs one of the three tracked actions against the phishing
// server over HTTP, exactly as a recipient's mail client or browser would.
func fireAction(t *testing.T, ctx *testContext, campaign models.Campaign, rid, action string) {
	t.Helper()
	switch action {
	case "open":
		// The 1x1 pixel embedded in the email body.
		openEmail(t, ctx, rid)
	case "attachment":
		// The remote resource embedded in the attachment.
		openAttachment(t, ctx, rid, "")
	case "click":
		// The {{.URL}} link in the email body.
		clickLink(t, ctx, rid, campaign.Page.HTML)
	default:
		t.Fatalf("unknown action %q", action)
	}
}

// TestAllThreeActionsInOneEmail drives all three tracking endpoints for a
// single recipient, as a single email carrying a tracking pixel, a tracked
// attachment and a link would, and asserts each action is recorded separately.
func TestAllThreeActionsInOneEmail(t *testing.T) {
	ctx := setupTest(t)
	defer tearDown(t, ctx)
	campaign := getFirstCampaign(t)
	result := campaign.Results[0]

	fireAction(t, ctx, campaign, result.RId, "open")
	fireAction(t, ctx, campaign, result.RId, "attachment")
	fireAction(t, ctx, campaign, result.RId, "click")

	campaign = getFirstCampaign(t)
	got := campaign.Results[0]
	if !got.EmailOpened {
		t.Fatalf("email open was not recorded")
	}
	if !got.AttachmentOpened {
		t.Fatalf("attachment open was not recorded")
	}
	if !got.ClickedLink {
		t.Fatalf("link click was not recorded")
	}
	if got.SubmittedData {
		t.Fatalf("submitted_data should not be set, no form was submitted")
	}
	if got.Status != models.EventClicked {
		t.Fatalf("Status should report the furthest step. expected %s got %s",
			models.EventClicked, got.Status)
	}

	// Every action must also appear in the timeline, since that is what the
	// per-recipient detail view and the raw event export read.
	wantEvents := map[string]bool{
		models.EventOpened:           false,
		models.EventAttachmentOpened: false,
		models.EventClicked:          false,
	}
	for _, event := range campaign.Events {
		if _, ok := wantEvents[event.Message]; ok {
			wantEvents[event.Message] = true
		}
	}
	for message, seen := range wantEvents {
		if !seen {
			t.Fatalf("expected a %q event in the campaign timeline", message)
		}
	}
}

// TestActionOrderDoesNotMatter runs every permutation of the three actions
// over HTTP. None of them may suppress another.
func TestActionOrderDoesNotMatter(t *testing.T) {
	orders := [][]string{
		{"open", "attachment", "click"},
		{"open", "click", "attachment"},
		{"click", "open", "attachment"},
		{"click", "attachment", "open"},
		{"attachment", "open", "click"},
		{"attachment", "click", "open"},
	}
	for _, order := range orders {
		t.Run(fmt.Sprintf("%s_%s_%s", order[0], order[1], order[2]), func(t *testing.T) {
			ctx := setupTest(t)
			defer tearDown(t, ctx)
			campaign := getFirstCampaign(t)
			rid := campaign.Results[0].RId

			for _, action := range order {
				fireAction(t, ctx, campaign, rid, action)
			}

			got := getFirstCampaign(t).Results[0]
			if !got.EmailOpened || !got.AttachmentOpened || !got.ClickedLink {
				t.Fatalf("order %v lost an action: email_opened=%v attachment_opened=%v clicked_link=%v",
					order, got.EmailOpened, got.AttachmentOpened, got.ClickedLink)
			}
		})
	}
}

// TestConcurrentTrackingRequestsDoNotClobber fires the three endpoints at the
// same time. A mail client that prefetches remote content will do exactly
// this, and before the handlers moved to targeted column updates the three
// full-row writes raced and dropped flags.
func TestConcurrentTrackingRequestsDoNotClobber(t *testing.T) {
	ctx := setupTest(t)
	defer tearDown(t, ctx)
	campaign := getFirstCampaign(t)
	rid := campaign.Results[0].RId

	urls := []string{
		fmt.Sprintf("%s/track?%s=%s", ctx.phishServer.URL, models.RecipientParameter, rid),
		fmt.Sprintf("%s/track/attachment?%s=%s", ctx.phishServer.URL, models.RecipientParameter, rid),
		fmt.Sprintf("%s/?%s=%s", ctx.phishServer.URL, models.RecipientParameter, rid),
	}

	var wg sync.WaitGroup
	for _, url := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			resp, err := http.Get(url)
			if err != nil {
				t.Errorf("error requesting %s: %v", url, err)
				return
			}
			resp.Body.Close()
		}(url)
	}
	wg.Wait()

	got := getFirstCampaign(t).Results[0]
	if !got.EmailOpened || !got.AttachmentOpened || !got.ClickedLink {
		t.Fatalf("concurrent requests lost an action: email_opened=%v attachment_opened=%v clicked_link=%v",
			got.EmailOpened, got.AttachmentOpened, got.ClickedLink)
	}
}

// TestAttachmentOnlyRecipientIsNotCountedAsOpened covers the recipient whose
// mail client blocked the body pixel but who still opened the attachment.
func TestAttachmentOnlyRecipientIsNotCountedAsOpened(t *testing.T) {
	ctx := setupTest(t)
	defer tearDown(t, ctx)
	campaign := getFirstCampaign(t)
	rid := campaign.Results[0].RId

	fireAction(t, ctx, campaign, rid, "attachment")

	got := getFirstCampaign(t).Results[0]
	if got.EmailOpened {
		t.Fatalf("attachment open must not set email_opened")
	}
	if !got.AttachmentOpened {
		t.Fatalf("attachment open was not recorded")
	}
	if got.Status != models.StatusSending {
		t.Fatalf("attachment opens must not advance Status. expected %s got %s",
			models.StatusSending, got.Status)
	}
}

// TestCampaignSummaryReportsEachAction checks the numbers the dashboard, the
// campaign list and the REST API all read, including the two-column opened
// figure.
func TestCampaignSummaryReportsEachAction(t *testing.T) {
	ctx := setupTest(t)
	defer tearDown(t, ctx)
	campaign := getFirstCampaign(t)
	if len(campaign.Results) < 2 {
		t.Fatalf("expected at least two recipients in the test campaign")
	}

	// First recipient does everything, second only opens the attachment.
	fireAction(t, ctx, campaign, campaign.Results[0].RId, "open")
	fireAction(t, ctx, campaign, campaign.Results[0].RId, "attachment")
	fireAction(t, ctx, campaign, campaign.Results[0].RId, "click")
	fireAction(t, ctx, campaign, campaign.Results[1].RId, "attachment")

	summary, err := models.GetCampaignSummary(campaign.Id, 1)
	if err != nil {
		t.Fatalf("error getting campaign summary: %v", err)
	}
	if summary.Stats.OpenedEmail != 1 {
		t.Fatalf("unexpected measured opened count. expected 1 got %d", summary.Stats.OpenedEmail)
	}
	if summary.Stats.AttachmentOpened != 2 {
		t.Fatalf("unexpected attachment opened count. expected 2 got %d", summary.Stats.AttachmentOpened)
	}
	if summary.Stats.ClickedLink != 1 {
		t.Fatalf("unexpected clicked count. expected 1 got %d", summary.Stats.ClickedLink)
	}
	if summary.Stats.EngagedEmail != 2 {
		t.Fatalf("unexpected engaged count. expected 2 got %d", summary.Stats.EngagedEmail)
	}
}
