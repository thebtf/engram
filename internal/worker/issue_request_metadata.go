package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func issueSessionQuerySelector(query url.Values, key string) (string, error) {
	values, present := query[key]
	if !present {
		return "", nil
	}
	if len(values) != 1 || values[0] == "" || gormdb.ValidateIssueSessionSelector(values[0]) != nil {
		return "", fmt.Errorf("%w: %s must be one nonempty bounded session identifier", gormdb.ErrIssueInvalidInput, key)
	}
	return values[0], nil
}

func issueHistoryQuery(r *http.Request) (int, int64, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: invalid issue history query", gormdb.ErrIssueInvalidInput)
	}
	limit := int64(gormdb.IssueHistoryDefaultLimit)
	if values, present := query["history_limit"]; present {
		if len(values) != 1 {
			return 0, 0, fmt.Errorf("%w: history_limit must occur once", gormdb.ErrIssueInvalidInput)
		}
		limit, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || limit < 1 || limit > gormdb.IssueHistoryMaxLimit {
			return 0, 0, fmt.Errorf("%w: history_limit must be an integer between 1 and %d", gormdb.ErrIssueInvalidInput, gormdb.IssueHistoryMaxLimit)
		}
	}
	var after int64
	if values, present := query["history_after_id"]; present {
		if len(values) != 1 {
			return 0, 0, fmt.Errorf("%w: history_after_id must occur once", gormdb.ErrIssueInvalidInput)
		}
		after, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || after < 0 {
			return 0, 0, fmt.Errorf("%w: history_after_id must be a nonnegative integer", gormdb.ErrIssueInvalidInput)
		}
	}
	return int(limit), after, nil
}

// Omitted preserves the recipient; explicit null or empty clears it. This is a
// source-authority field mutation, never a new authorization or liveness rule.
func issueRecipientPatch(raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, fmt.Errorf("%w: target_session must be a string or null", gormdb.ErrIssueInvalidInput)
	}
	if err := gormdb.ValidateIssueSessionSelector(value); err != nil {
		return nil, err
	}
	return &value, nil
}
