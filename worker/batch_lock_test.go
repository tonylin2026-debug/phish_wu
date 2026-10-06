package worker

import (
	"testing"
	"time"

	"github.com/gophish/gophish/mailer"
	"github.com/gophish/gophish/models"
)

// TestProcessCampaignsUnlocksTheBatchOnError asserts that a failure partway
// through processCampaigns does not strand the maillogs it has already locked.
//
// processCampaigns locks the entire batch up front, then looks up the campaign
// context for each maillog in turn. A lookup failure returned immediately,
// leaving every maillog in that minute's batch with processing set. Since
// GetQueuedMailLogs only ever returns rows that are not being processed, those
// emails were never picked up again; the only thing that releases them is
// UnlockAllMailLogs, which runs once at startup. One bad row therefore silently
// stopped every other campaign in the same batch until gophish was restarted.
func TestProcessCampaignsUnlocksTheBatchOnError(t *testing.T) {
	setupTest(t)

	// A healthy campaign, whose maillogs share the batch with the bad row.
	campaign, err := setupCampaign(1)
	if err != nil {
		t.Fatalf("error creating campaign: %v", err)
	}
	ms, err := models.GetMailLogsByCampaign(campaign.Id)
	if err != nil {
		t.Fatalf("error getting maillogs for campaign: %v", err)
	}
	for _, m := range ms {
		m.Unlock()
	}

	// A maillog pointing at a campaign that does not exist, which is what makes
	// the context lookup fail partway through the batch.
	orphan := &models.Campaign{Id: 999999, UserId: 1}
	err = models.GenerateMailLog(orphan, &models.Result{RId: "orphaned-result"},
		time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("error generating the orphaned maillog: %v", err)
	}

	queued, err := models.GetQueuedMailLogs(time.Now().UTC())
	if err != nil {
		t.Fatalf("error reading the queue: %v", err)
	}
	before := len(queued)
	if before < 2 {
		t.Fatalf("expected the campaign maillogs and the orphan to be queued, got %d", before)
	}

	worker := &DefaultWorker{mailer: &logMailer{queue: make(chan []mailer.Mail, 16)}}
	if err := worker.processCampaigns(time.Now().UTC()); err == nil {
		t.Fatal("expected processCampaigns to report the failed campaign lookup")
	}

	queued, err = models.GetQueuedMailLogs(time.Now().UTC())
	if err != nil {
		t.Fatalf("error reading the queue: %v", err)
	}
	if len(queued) != before {
		t.Fatalf("processCampaigns failed and left %d of %d maillogs locked; they stay "+
			"unsendable until gophish is restarted", before-len(queued), before)
	}
}
