// Code generated from the frozen engine.Controller contract. DO NOT EDIT.
package daemon

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func (m *managedController) Status() engine.Status { return m.controller().Status() }
func (m *managedController) Mode() engine.Mode     { return m.controller().Mode() }
func (m *managedController) Account(ctx context.Context) (engine.Account, error) {
	return m.controller().Account(ctx)
}
func (m *managedController) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	return m.controller().Diagnostics(ctx)
}
func (m *managedController) Resolve(ctx context.Context, req engine.ResolveRequest) ([]engine.Candidate, error) {
	return m.controller().Resolve(ctx, req)
}
func (m *managedController) ResolveContacts(ctx context.Context, queries []string) ([]engine.ContactResolution, error) {
	return m.controller().ResolveContacts(ctx, queries)
}
func (m *managedController) RefreshDialogs(ctx context.Context, req engine.RefreshRequest) (engine.RefreshResult, error) {
	return m.controller().RefreshDialogs(ctx, req)
}
func (m *managedController) RefreshGroup(ctx context.Context, req engine.GroupRequest) (engine.Group, error) {
	return m.controller().RefreshGroup(ctx, req)
}
func (m *managedController) SearchRemote(ctx context.Context, req engine.SearchRequest) (engine.SearchResult, error) {
	return m.controller().SearchRemote(ctx, req)
}
func (m *managedController) FetchWindow(ctx context.Context, req engine.WindowRequest) ([]engine.Message, error) {
	return m.controller().FetchWindow(ctx, req)
}
func (m *managedController) PollResults(ctx context.Context, req engine.PollRequest) (engine.PollResults, error) {
	return m.controller().PollResults(ctx, req)
}
func (m *managedController) InviteLink(ctx context.Context, req engine.InviteLinkRequest) (engine.InviteLinkResult, error) {
	return m.controller().InviteLink(ctx, req)
}
func (m *managedController) Download(ctx context.Context, req engine.DownloadRequest) (engine.Download, error) {
	return m.controller().Download(ctx, req)
}
func (m *managedController) ProfilePhoto(ctx context.Context, req engine.PhotoRequest) (engine.Download, error) {
	return m.controller().ProfilePhoto(ctx, req)
}
func (m *managedController) TranscribeNative(ctx context.Context, key engine.MessageKey) (engine.NativeTranscript, error) {
	return m.controller().TranscribeNative(ctx, key)
}
func (m *managedController) Backfill(ctx context.Context, req engine.BackfillRequest) (engine.BackfillResult, error) {
	return m.controller().Backfill(ctx, req)
}
func (m *managedController) SendText(ctx context.Context, req engine.SendTextRequest) (engine.WriteResult, error) {
	return m.controller().SendText(ctx, req)
}
func (m *managedController) SendMedia(ctx context.Context, req engine.SendMediaRequest) (engine.WriteResult, error) {
	return m.controller().SendMedia(ctx, req)
}
func (m *managedController) SendLocation(ctx context.Context, req engine.SendLocationRequest) (engine.WriteResult, error) {
	return m.controller().SendLocation(ctx, req)
}
func (m *managedController) SendPoll(ctx context.Context, req engine.SendPollRequest) (engine.WriteResult, error) {
	return m.controller().SendPoll(ctx, req)
}
func (m *managedController) Edit(ctx context.Context, req engine.EditRequest) (engine.WriteResult, error) {
	return m.controller().Edit(ctx, req)
}
func (m *managedController) Delete(ctx context.Context, req engine.DeleteRequest) (engine.WriteResult, error) {
	return m.controller().Delete(ctx, req)
}
func (m *managedController) React(ctx context.Context, req engine.ReactRequest) (engine.WriteResult, error) {
	return m.controller().React(ctx, req)
}
func (m *managedController) Forward(ctx context.Context, req engine.ForwardRequest) (engine.WriteResult, error) {
	return m.controller().Forward(ctx, req)
}
func (m *managedController) MarkRead(ctx context.Context, req engine.MarkReadRequest) (engine.WriteResult, error) {
	return m.controller().MarkRead(ctx, req)
}
func (m *managedController) SetTyping(ctx context.Context, req engine.TypingRequest) (engine.WriteResult, error) {
	return m.controller().SetTyping(ctx, req)
}
func (m *managedController) Organise(ctx context.Context, req engine.OrganiseRequest) (engine.WriteResult, error) {
	return m.controller().Organise(ctx, req)
}
func (m *managedController) ManageParticipants(ctx context.Context, req engine.ParticipantsRequest) (engine.ParticipantsResult, error) {
	return m.controller().ManageParticipants(ctx, req)
}
func (m *managedController) UpdateGroup(ctx context.Context, req engine.UpdateGroupRequest) (engine.WriteResult, error) {
	return m.controller().UpdateGroup(ctx, req)
}
func (m *managedController) Leave(ctx context.Context, req engine.LeaveRequest) (engine.WriteResult, error) {
	return m.controller().Leave(ctx, req)
}
func (m *managedController) SubmitCode(ctx context.Context, code engine.Secret) error {
	return m.controller().SubmitCode(ctx, code)
}
func (m *managedController) SubmitPassword(ctx context.Context, password engine.Secret) error {
	return m.controller().SubmitPassword(ctx, password)
}
func (m *managedController) ResendCode(ctx context.Context) error {
	return m.controller().ResendCode(ctx)
}
func (m *managedController) CancelLogin()        { m.controller().CancelLogin() }
func (m *managedController) Login() engine.Login { return m.controller().Login() }
func (m *managedController) Disconnect(ctx context.Context, reason string) error {
	return m.controller().Disconnect(ctx, reason)
}
func (m *managedController) Connect(ctx context.Context) error { return m.controller().Connect(ctx) }
