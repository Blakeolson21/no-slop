package cli

import (
	"fmt"
	"github.com/Blakeolson21/no-slop/internal/db"
	"strings"
	"testing"
)

func TestReviewTimingFieldRetainsMillisecondStart(t *testing.T) {
	ms := int64(1000999)
	field := reviewTimingField(&db.ReviewTiming{StartedAt: 1000, StartedAtMS: &ms, TotalMS: 200})
	if !strings.Contains(fmt.Sprint(field.Value), "1970-01-01T00:16:40.999Z") {
		t.Fatalf("start precision lost: %v", field.Value)
	}
}
