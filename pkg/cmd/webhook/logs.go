package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// logTimeLayouts are read by the server in its own zone.
var logTimeLayouts = []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02 15:04:05", time.RFC3339}

// logTimeFlag validates --since/--until; a duration is sent in UTC so the server cannot reinterpret its zone.
func logTimeFlag(flag, v string, allowDuration bool) (string, error) {
	v = strings.TrimSpace(v)
	if allowDuration {
		if d, err := time.ParseDuration(v); err == nil {
			if d <= 0 {
				return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s must be a positive duration or a date, e.g. 24h or 2026-11-01", flag)}
			}
			return time.Now().UTC().Add(-d).Format(time.RFC3339), nil
		}
	}
	for _, layout := range logTimeLayouts {
		if _, err := time.Parse(layout, v); err == nil {
			return v, nil
		}
	}
	want := "a date or datetime, e.g. 2026-11-01 or \"2026-11-01 18:30\""
	if allowDuration {
		want = "a duration or a date, e.g. 24h or 2026-11-01"
	}
	return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s must be %s, got %q", flag, want, v)}
}

// logRangeReversed reports a since after until. A duration resolves to an RFC3339 instant and the
// server reads a date in its own zone, so both sides are compared in that zone; a bare until date
// covers its whole day.
func logRangeReversed(since, until string) bool {
	if since == "" || until == "" {
		return false
	}
	parse := func(v string) (time.Time, string, bool) {
		for _, layout := range logTimeLayouts {
			if t, err := time.ParseInLocation(layout, v, serverZone); err == nil {
				return t, layout, true
			}
		}
		return time.Time{}, "", false
	}
	start, _, ok := parse(since)
	end, layout, ok2 := parse(until)
	if !ok || !ok2 {
		return false
	}
	if layout == "2006-01-02" {
		end = end.Add(24*time.Hour - time.Nanosecond)
	}
	return start.After(end)
}

// serverZone is the zone the server reads a log date in (+07).
var serverZone = time.FixedZone("ICT", 7*60*60)

func newLogsCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		webhookXID, transactionID, status, since, until string
		perPage, page                                   int
	)
	cmd := &cobra.Command{
		Use:     "logs [webhook-xid]",
		Aliases: []string{"log"},
		Short:   "List webhook delivery logs",
		Long: `List delivery logs, newest first. With no argument it lists every webhook's;
an xid narrows it to one, the same as --webhook. --transaction narrows it to the
deliveries of one transaction (the txn= value "webhook listen" prints).

A delivery is addressed by its transaction and webhook: pass both to
"sepay webhook replay" to re-send it. --since takes a duration (24h) or a date or
datetime; --until a date or datetime, a bare date meaning the end of that day.`,
		Example: `  sepay webhook logs --since 24h --status failed
  sepay webhook logs <webhook-xid> --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", []string{"success", "failed", logStatusListen, "skipped"}); err != nil {
				return err
			}
			// Accept the positional id so cobra does not answer "unknown command <xid>".
			if len(args) > 0 {
				if webhookXID != "" {
					return &cmdutil.FlagError{Err: errors.New("pass the webhook xid as an argument or --webhook, not both")}
				}
				webhookXID = args[0]
			}
			q := url.Values{}
			if webhookXID != "" {
				q.Set("webhook_xid", webhookXID)
			}
			if v := strings.TrimSpace(transactionID); v != "" {
				q.Set("transaction_id", v)
			} else if cmd.Flags().Changed("transaction") {
				return &cmdutil.FlagError{Err: errors.New("--transaction needs a transaction id")}
			}
			if status != "" {
				q.Set("status", status)
			}
			if cmd.Flags().Changed("since") || cmd.Flags().Changed("from") {
				v, err := logTimeFlag(cmdutil.SpelledAs(cmd, "since"), since, true)
				if err != nil {
					return err
				}
				q.Set("since", v)
			}
			if cmd.Flags().Changed("until") || cmd.Flags().Changed("to") {
				v, err := logTimeFlag(cmdutil.SpelledAs(cmd, "until"), until, false)
				if err != nil {
					return err
				}
				q.Set("until", v)
			}
			if logRangeReversed(q.Get("since"), q.Get("until")) {
				return &cmdutil.FlagError{Err: fmt.Errorf("--%s must not be after --%s", cmdutil.SpelledAs(cmd, "since"), cmdutil.SpelledAs(cmd, "until"))}
			}
			if perPage > 0 {
				q.Set("per_page", strconv.Itoa(perPage))
			}
			if page > 0 {
				q.Set("page", strconv.Itoa(page))
			}
			return runLogs(cmd.Context(), f, q)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&webhookXID, "webhook", "", "Filter by webhook xid")
	fl.StringVar(&transactionID, "transaction", "", "Filter by transaction id")
	fl.StringVar(&status, "status", "", "Filter by delivery status: success|failed|listen|skipped")
	fl.StringVar(&since, "since", "", "Only logs from this point: a duration (24h) or a date/datetime")
	fl.StringVar(&until, "until", "", "Only logs up to this date/datetime (a date means its end)")
	cmdutil.AliasFlag(fl, "from", "since")
	cmdutil.AliasFlag(fl, "to", "until")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	fl.IntVar(&page, "page", 0, "Page number")
	return cmd
}

func runLogs(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/webhook-logs", query)
	if err != nil {
		return err
	}
	logs, page, err := api.ListInto[Log](env)
	if err != nil {
		return err
	}

	if err := f.RenderOutput(logs, func() error {
		if len(logs) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No webhook logs found.")
			return nil
		}
		t := jsonfmt.NewTable("DATE", "STATUS", "CODE", "REPLAY", "TRANSACTION", "WEBHOOK", "URL").Truncatable("URL")
		for _, l := range logs {
			t.AddRow(l.CreatedAt, logStatus(f, l.Status), intOrDash(l.ResponseStatusCode), f.YesNo(l.IsReplay),
				jsonfmt.OrDash(strOrEmpty(l.TransactionID)), jsonfmt.OrDash(strOrEmpty(l.WebhookXID)), l.urlLabel())
		}
		return f.RenderTable(t)
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(logs))
	return nil
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func logStatus(f *cmdutil.Factory, status string) string {
	switch status {
	case "success", logStatusListen:
		return f.IOStreams.Green(status)
	case "failed":
		return f.IOStreams.Red(status)
	case "skipped":
		// The webhook's own filters dropped the event before any request.
		return f.IOStreams.Dim(status)
	}
	return status
}
