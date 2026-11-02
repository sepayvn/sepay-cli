package bankhub

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// simResult is the simulate-transaction (POST v1/transaction/create) payload.
type simResult struct {
	TransactionID   string      `json:"transaction_id" yaml:"transaction_id"`
	ReferenceNumber string      `json:"reference_number" yaml:"reference_number"`
	TransactionDate string      `json:"transaction_date" yaml:"transaction_date"`
	Amount          api.FlexInt `json:"amount" yaml:"amount"`
	TransferType    string      `json:"transfer_type" yaml:"transfer_type"`
	Accumulated     api.FlexInt `json:"accumulated" yaml:"accumulated"`
}

func newSandboxCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "sandbox <command>", Short: "Bank Hub sandbox simulation"}

	linkRequest := &cobra.Command{Use: "link-request <command>", Short: "Accept or reject a sandbox link request"}
	linkRequest.AddCommand(
		cmdutil.Stub(&cobra.Command{Use: "accept <id>", Short: "Accept a link request", Args: cobra.ExactArgs(1)}),
		cmdutil.Stub(&cobra.Command{Use: "reject <id>", Short: "Reject a link request", Args: cobra.ExactArgs(1)}),
	)
	cmdutil.Stub(linkRequest)
	cmd.AddCommand(newSimulateTransactionCmd(f), linkRequest)
	return cmd
}

func newSimulateTransactionCmd(f *cmdutil.Factory) *cobra.Command {
	var bankAccount, transferType, content, reference string
	var amt int64
	cmd := &cobra.Command{
		Use:   "simulate-transaction",
		Short: "Simulate an inbound transaction on a bank account (sandbox)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := cmdutil.ValidateChoice(cmd, "type", transferTypes); err != nil {
				return err
			}
			ba, err := pickBankAccount(ctx, f, bankAccount, "bank-account", "Select a bank account")
			if err != nil {
				return err
			}
			// Changed, not zero: "--amount 0" is a value.
			if !cmd.Flags().Changed("amount") {
				s, inErr := f.InputOrFlag("", "amount", "Amount (VND)")
				if inErr != nil {
					return inErr
				}
				if amt, inErr = strconv.ParseInt(strings.TrimSpace(s), 10, 64); inErr != nil {
					return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be an integer")}
				}
			}
			if amt <= 0 {
				return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be greater than 0")}
			}
			content, err = f.InputOrFlag(content, "content", "Transaction content")
			if err != nil {
				return err
			}
			if content == "" {
				return &cmdutil.FlagError{Err: fmt.Errorf("--content is required")}
			}
			return runSimulateTransaction(ctx, f, ba, transferType, amt, content, reference)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&bankAccount, "bank-account", "", "Bank account xid to credit/debit (required)")
	fl.StringVar(&transferType, "type", "credit", "Transfer type: "+strings.Join(transferTypes, "|"))
	fl.Int64Var(&amt, "amount", 0, "Amount in VND, greater than 0 (required)")
	fl.StringVar(&content, "content", "", "Transaction content (required)")
	fl.StringVar(&reference, "reference", "", "Reference number (optional)")
	return cmdutil.MarkRequired(cmd, "bank-account", "amount", "content")
}

func runSimulateTransaction(ctx context.Context, f *cmdutil.Factory, bankAccount, transferType string, amt int64, content, reference string) error {
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	body := map[string]any{
		"bank_account_xid":    bankAccount,
		"transfer_type":       transferType,
		"amount":              amt,
		"transaction_content": content,
	}
	if reference != "" {
		body["reference_number"] = reference
	}
	resp, err := client.Post(ctx, "/v1/transaction/create", body)
	if err != nil {
		return err
	}
	r, err := ibh.DataInto[simResult](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(r, func() error {
		f.Printf(
			"%s mock transaction created\nTransaction: %s\nReference:   %s\nDate:        %s\nType:        %s\nAmount:      %s\nBalance:     %s\n",
			f.SuccessIcon(), r.TransactionID, jsonfmt.OrDash(r.ReferenceNumber), r.TransactionDate, r.TransferType,
			jsonfmt.FormatVND(int64(r.Amount)), jsonfmt.FormatVND(int64(r.Accumulated)))
		return nil
	})
}
