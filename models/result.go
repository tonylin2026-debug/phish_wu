package models

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/oschwald/maxminddb-golang"
)

// geoDatabasePath is the location of the MaxMind GeoLite2 city database used
// to geolocate the IP addresses seen by the phishing server.
const geoDatabasePath = "static/db/geolite2-city.mmdb"

// Column names for the independent action flags. These are referenced by name
// because the flags are written with targeted UPDATE statements rather than a
// full-row save - see recordAction.
const (
	columnEmailOpened      = "email_opened"
	columnAttachmentOpened = "attachment_opened"
	columnClickedLink      = "clicked_link"
	columnSubmittedData    = "submitted_data"
	columnReported         = "reported"
)

var (
	geoMu     sync.Mutex
	geoReader *maxminddb.Reader
)

// openGeoDatabase returns a shared, lazily opened handle to the GeoIP
// database. The reader is safe for concurrent use and is kept open for the
// lifetime of the process.
//
// The previous implementation opened and closed the 38MB database on every
// single tracked request, and called log.Fatal when the file could not be
// read - which meant one unreadable file could terminate the phishing server
// from inside a request handler. Failures are now returned to the caller and
// a later request is free to retry.
func openGeoDatabase() (*maxminddb.Reader, error) {
	geoMu.Lock()
	defer geoMu.Unlock()
	if geoReader != nil {
		return geoReader, nil
	}
	reader, err := maxminddb.Open(geoDatabasePath)
	if err != nil {
		return nil, err
	}
	geoReader = reader
	return geoReader, nil
}

type mmCity struct {
	GeoPoint mmGeoPoint `maxminddb:"location"`
}

type mmGeoPoint struct {
	Latitude  float64 `maxminddb:"latitude"`
	Longitude float64 `maxminddb:"longitude"`
}

// Result contains the fields for a result object,
// which is a representation of a target in a campaign.
//
// Status is an ordered state machine describing the furthest step a recipient
// reached (Sent -> Email Opened -> Clicked Link -> Submitted Data). Because it
// is a single value it cannot express that a recipient both opened the email
// and opened an attachment and clicked the link.
//
// EmailOpened, AttachmentOpened, ClickedLink and SubmittedData are therefore
// stored independently. They are plain facts: each is set when the
// corresponding action is observed and is never cleared or overwritten by a
// different action. Reported already worked this way.
type Result struct {
	Id         int64  `json:"-"`
	CampaignId int64  `json:"-"`
	UserId     int64  `json:"-"`
	RId        string `json:"id"`
	Status     string `json:"status" sql:"not null"`

	// Independent action flags - see the type comment.
	EmailOpened      bool `json:"email_opened"`
	AttachmentOpened bool `json:"attachment_opened"`
	ClickedLink      bool `json:"clicked_link"`
	SubmittedData    bool `json:"submitted_data"`
	Reported         bool `json:"reported" sql:"not null"`

	IP           string    `json:"ip"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	SendDate     time.Time `json:"send_date"`
	ModifiedDate time.Time `json:"modified_date"`
	BaseRecipient
}

// Engaged reports whether the recipient interacted with the campaign in any
// way at all. It is the union of every observed action, and is what the
// "reached" figure in the campaign summary counts.
//
// This matters because mail clients routinely block remote images, so a
// recipient can click the link or open an attachment without the tracking
// pixel ever loading. EmailOpened stays an honest measurement of the pixel;
// Engaged is the number most campaigns actually care about.
func (r *Result) Engaged() bool {
	return r.EmailOpened || r.AttachmentOpened || r.ClickedLink || r.SubmittedData
}

func (r *Result) createEvent(status string, details interface{}) (*Event, error) {
	e := &Event{Email: r.Email, Message: status}
	if details != nil {
		dj, err := json.Marshal(details)
		if err != nil {
			return nil, err
		}
		e.Details = string(dj)
	}
	if err := AddEvent(e, r.CampaignId); err != nil {
		return nil, err
	}
	return e, nil
}

// recordAction atomically records one or more independent action flags,
// writing only the named columns.
//
// Opening the email, opening an attachment and clicking the link are three
// separate HTTP requests which can arrive concurrently for the same
// recipient. A read-modify-write through db.Save() writes every column back
// from a potentially stale in-memory snapshot, so two concurrent requests
// would silently drop one another's flags. Updating just these columns cannot
// lose a write made by another request.
func (r *Result) recordAction(at time.Time, columns ...string) error {
	updates := map[string]interface{}{"modified_date": at}
	for _, column := range columns {
		updates[column] = true
	}
	err := db.Table("results").Where("id = ?", r.Id).Updates(updates).Error
	if err != nil {
		return err
	}
	r.ModifiedDate = at
	for _, column := range columns {
		switch column {
		case columnEmailOpened:
			r.EmailOpened = true
		case columnAttachmentOpened:
			r.AttachmentOpened = true
		case columnClickedLink:
			r.ClickedLink = true
		case columnSubmittedData:
			r.SubmittedData = true
		case columnReported:
			r.Reported = true
		}
	}
	return nil
}

// advanceStatus moves Status forward through the ordered progression without
// ever letting it regress. The guard is expressed in the WHERE clause so the
// check and the write happen as a single atomic statement instead of a racy
// read-then-write.
func (r *Result) advanceStatus(status string, at time.Time, blockedBy ...string) error {
	query := db.Table("results").Where("id = ?", r.Id)
	if len(blockedBy) > 0 {
		query = query.Where("status NOT IN (?)", blockedBy)
	}
	tx := query.Updates(map[string]interface{}{
		"status":        status,
		"modified_date": at,
	})
	if tx.Error != nil {
		return tx.Error
	}
	// No rows updated means the guard fired and the recipient is already
	// further along, so the in-memory status stays as it was.
	if tx.RowsAffected > 0 {
		r.Status = status
		r.ModifiedDate = at
	}
	return nil
}

// HandleEmailSent updates a Result to indicate that the email has been
// successfully sent to the remote SMTP server
func (r *Result) HandleEmailSent() error {
	event, err := r.createEvent(EventSent, nil)
	if err != nil {
		return err
	}
	err = db.Table("results").Where("id = ?", r.Id).Updates(map[string]interface{}{
		"status":        EventSent,
		"send_date":     event.Time,
		"modified_date": event.Time,
	}).Error
	if err != nil {
		return err
	}
	r.SendDate = event.Time
	r.Status = EventSent
	r.ModifiedDate = event.Time
	return nil
}

// HandleEmailError updates a Result to indicate that there was an error when
// attempting to send the email to the remote SMTP server.
func (r *Result) HandleEmailError(sendErr error) error {
	event, err := r.createEvent(EventSendingError, EventError{Error: sendErr.Error()})
	if err != nil {
		return err
	}
	err = db.Table("results").Where("id = ?", r.Id).Updates(map[string]interface{}{
		"status":        Error,
		"modified_date": event.Time,
	}).Error
	if err != nil {
		return err
	}
	r.Status = Error
	r.ModifiedDate = event.Time
	return nil
}

// HandleEmailBackoff updates a Result to indicate that the email received a
// temporary error and needs to be retried
func (r *Result) HandleEmailBackoff(sendErr error, sendDate time.Time) error {
	event, err := r.createEvent(EventSendingError, EventError{Error: sendErr.Error()})
	if err != nil {
		return err
	}
	err = db.Table("results").Where("id = ?", r.Id).Updates(map[string]interface{}{
		"status":        StatusRetry,
		"send_date":     sendDate,
		"modified_date": event.Time,
	}).Error
	if err != nil {
		return err
	}
	r.Status = StatusRetry
	r.SendDate = sendDate
	r.ModifiedDate = event.Time
	return nil
}

// HandleEmailOpened updates a Result in the case where the recipient opened the
// email.
func (r *Result) HandleEmailOpened(details EventDetails) error {
	event, err := r.createEvent(EventOpened, details)
	if err != nil {
		return err
	}
	// The flag is a fact and is always recorded, even when the recipient has
	// already clicked the link - the two are independent actions.
	if err = r.recordAction(event.Time, columnEmailOpened); err != nil {
		return err
	}
	// Status keeps its "furthest step reached" meaning for the existing UI and
	// CSV exports, so it must not regress once the link has been clicked.
	return r.advanceStatus(EventOpened, event.Time, EventClicked, EventDataSubmit)
}

// HandleAttachmentOpened records an attachment-open event. Attachment opens
// sit entirely outside the Sent -> Opened -> Clicked -> Submitted progression,
// so Status is deliberately left untouched: a recipient who opens an
// attachment after clicking the link must not have their Clicked Link status
// replaced, and a recipient who only opens the attachment must not be
// misreported as having opened the email.
func (r *Result) HandleAttachmentOpened(details EventDetails) error {
	event, err := r.createEvent(EventAttachmentOpened, details)
	if err != nil {
		return err
	}
	return r.recordAction(event.Time, columnAttachmentOpened)
}

// HandleClickedLink updates a Result in the case where the recipient clicked
// the link in an email.
func (r *Result) HandleClickedLink(details EventDetails) error {
	event, err := r.createEvent(EventClicked, details)
	if err != nil {
		return err
	}
	if err = r.recordAction(event.Time, columnClickedLink); err != nil {
		return err
	}
	return r.advanceStatus(EventClicked, event.Time, EventDataSubmit)
}

// HandleFormSubmit updates a Result in the case where the recipient submitted
// credentials to the form on a Landing Page.
func (r *Result) HandleFormSubmit(details EventDetails) error {
	event, err := r.createEvent(EventDataSubmit, details)
	if err != nil {
		return err
	}
	// Submitting the form necessarily means the link was followed, even if the
	// click itself was never separately recorded.
	if err = r.recordAction(event.Time, columnSubmittedData, columnClickedLink); err != nil {
		return err
	}
	return r.advanceStatus(EventDataSubmit, event.Time)
}

// HandleEmailReport updates a Result in the case where they report a simulated
// phishing email using the HTTP handler.
func (r *Result) HandleEmailReport(details EventDetails) error {
	event, err := r.createEvent(EventReported, details)
	if err != nil {
		return err
	}
	return r.recordAction(event.Time, columnReported)
}

// UpdateGeo updates the latitude and longitude of the result in
// the database given an IP address
func (r *Result) UpdateGeo(addr string) error {
	mmdb, err := openGeoDatabase()
	if err != nil {
		return fmt.Errorf("unable to open GeoIP database: %v", err)
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return fmt.Errorf("%s is not a valid IP address", addr)
	}
	var city mmCity
	if err = mmdb.Lookup(ip, &city); err != nil {
		return err
	}
	// Targeted update, not a full-row save. setupContext calls UpdateGeo before
	// the action handler runs, so writing the whole row here would clobber
	// action flags set by a concurrent request for the same recipient.
	err = db.Table("results").Where("id = ?", r.Id).Updates(map[string]interface{}{
		"ip":        addr,
		"latitude":  city.GeoPoint.Latitude,
		"longitude": city.GeoPoint.Longitude,
	}).Error
	if err != nil {
		return err
	}
	r.IP = addr
	r.Latitude = city.GeoPoint.Latitude
	r.Longitude = city.GeoPoint.Longitude
	return nil
}

func generateResultId() (string, error) {
	const alphaNum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	k := make([]byte, 7)
	for i := range k {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphaNum))))
		if err != nil {
			return "", err
		}
		k[i] = alphaNum[idx.Int64()]
	}
	return string(k), nil
}

// GenerateId generates a unique key to represent the result
// in the database
func (r *Result) GenerateId(tx *gorm.DB) error {
	// Keep trying until we generate a unique key (shouldn't take more than one or two iterations)
	for {
		rid, err := generateResultId()
		if err != nil {
			return err
		}
		r.RId = rid
		err = tx.Table("results").Where("r_id=?", r.RId).First(&Result{}).Error
		if err == gorm.ErrRecordNotFound {
			break
		}
		// Any other error means the uniqueness check itself failed; looping
		// would spin forever.
		if err != nil {
			return err
		}
	}
	return nil
}

// GetResult returns the Result object from the database
// given the ResultId
func GetResult(rid string) (Result, error) {
	r := Result{}
	err := db.Where("r_id=?", rid).First(&r).Error
	return r, err
}
