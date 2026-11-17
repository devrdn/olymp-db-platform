package api

// ArchiveWireTypes lets the external tests reflect over the archive's wire
// types, which are unexported.
var ArchiveWireTypes = map[string]any{
	"list item": archiveListItem{},
	"contest":   archiveContestResponse{},
	"tasks":     archiveTasksResponse{},
	"question":  archiveQuestionResponse{},
}
