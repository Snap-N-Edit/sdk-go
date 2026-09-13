// Package snapnedit is the official Go client for the snapnedit HTTP API —
// a self-hosted AI photo-editing platform.
//
// The API is a three-step flow: upload an image, create a job that names an
// operation, poll the job until it finishes, then download the result.
// [Client.Run] wraps all of it into one call; [Client.Upload],
// [Client.CreateJob], [Client.GetJob] and [Client.WaitForJob] expose the
// individual steps when you want to drive them yourself.
//
//	client := snapnedit.NewClient(os.Getenv("SNAPNEDIT_API_KEY"))
//
//	input, err := snapnedit.FromFile("photo.jpg")
//	if err != nil {
//		log.Fatal(err)
//	}
//	result, err := client.Run(context.Background(), snapnedit.OpRemoveBackground, input, nil)
//	if err != nil {
//		log.Fatal(err)
//	}
//	os.WriteFile("cutout.png", result.Output, 0o644)
//
// # Errors
//
// Every non-2xx API response and every terminally failed job surfaces as an
// [*Error] carrying a machine-readable [ErrorCode]. Branch on the code, never
// on the message:
//
//	if errors.Is(err, snapnedit.ErrPaymentRequired) {
//		// out of credits
//	}
//	var apiErr *snapnedit.Error
//	if errors.As(err, &apiErr) {
//		log.Printf("%s (%d): %s", apiErr.Code, apiErr.Status, apiErr.Message)
//	}
//
// # Bring your own storage
//
// A job's input can be a URL the server fetches ([FromURL]) instead of an
// upload, and its result can be PUT straight into a bucket you own — either a
// presigned URL you signed yourself ([PresignedPutDestination]) or one of your
// account's saved storage destinations ([SavedDestination], managed through
// [Client.Destinations]). With both, the image bytes never pass through your
// process.
//
// # Webhooks
//
// Outbound webhook signature verification lives in the sibling package
// github.com/Snap-N-Edit/sdk-go/webhooks.
//
// This package has no dependencies outside the Go standard library.
package snapnedit
