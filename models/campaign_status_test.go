package models

import (
	check "gopkg.in/check.v1"
)

// TestUpdateStatusDoesNotResurrectACompletedCampaign asserts that a campaign
// which has been completed cannot be moved back to an earlier status.
//
// The worker snapshots every campaign it is about to send for, then starts one
// goroutine per campaign. If an operator completes a campaign in the window
// between the snapshot being taken and the goroutine running, the goroutine
// still sees the stale Queued status and calls UpdateStatus(In progress).
// UpdateStatus had no guard in its WHERE clause, so the completed campaign was
// moved back to In progress while keeping its completed_date: a state the UI
// renders as still running, and which puts the campaign back on the wrong side
// of the completion check in the phishing server.
func (s *ModelsSuite) TestUpdateStatusDoesNotResurrectACompletedCampaign(c *check.C) {
	campaign := s.createCampaign(c)
	c.Assert(CompleteCampaign(campaign.Id, campaign.UserId), check.Equals, nil)

	completed, err := GetCampaign(campaign.Id, campaign.UserId)
	c.Assert(err, check.Equals, nil)
	c.Assert(completed.Status, check.Equals, CampaignComplete)

	// This is the call the worker goroutine makes from its stale snapshot.
	c.Assert(completed.UpdateStatus(CampaignInProgress), check.Equals, nil)

	got, err := GetCampaign(campaign.Id, campaign.UserId)
	c.Assert(err, check.Equals, nil)
	c.Assert(got.Status, check.Equals, CampaignComplete)
}
