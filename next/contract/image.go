package contract

const (
	MethodImageList   Method = "image.list"
	MethodImageRemove Method = "image.remove"
	MethodImageSave   Method = "image.save"
	MethodImageShow   Method = "image.show"
)

// CIRunnerImage names the image of a room that its CI runners use. Every other room image is a box image.
const CIRunnerImage = "ci-runner"

// RoomImage is one image a room keeps: a folder with a Containerfile and its build context.
type RoomImage struct {
	Name  string `json:"name"`
	Room  RoomID `json:"room"`
	ID    string `json:"id"` // content id of the folder; a save of changed contents changes it
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// ImageSaveRequest carries a gzip tar of an image's folder, which the caller packs because the engine cannot read its machine.
// Room is a session's own when empty, and the owner names one.
type ImageSaveRequest struct {
	Name    string `json:"name"`
	Archive []byte `json:"archive"`
	Room    RoomID `json:"room,omitempty"`
}

type ImageSaveResponse struct {
	Image RoomImage `json:"image"`
}

type ImageListRequest struct {
	Room RoomID `json:"room,omitempty"`
}

type ImageListResponse struct {
	Images []RoomImage `json:"images"`
}

type ImageShowRequest struct {
	Name string `json:"name"`
	Room RoomID `json:"room,omitempty"`
}

type ImageShowResponse struct {
	Image         RoomImage `json:"image"`
	Containerfile string    `json:"containerfile"`
	Files         []string  `json:"files"`
}

type ImageRemoveRequest struct {
	Name string `json:"name"`
	Room RoomID `json:"room,omitempty"`
}

type ImageRemoveResponse struct{}
