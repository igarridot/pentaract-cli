package pentaract

type LoginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

type Storage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type FSElement struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	IsFile bool   `json:"is_file"`
}

type UploadAccepted struct {
	UploadID string `json:"upload_id"`
}

type UploadProgress struct {
	Total             int64  `json:"total"`
	Uploaded          int64  `json:"uploaded"`
	TotalBytes        int64  `json:"total_bytes"`
	UploadedBytes     int64  `json:"uploaded_bytes"`
	VerificationTotal int64  `json:"verification_total"`
	Verified          int64  `json:"verified"`
	Status            string `json:"status"`
	WorkersStatus     string `json:"workers_status"`
	HasMetrics        bool   `json:"-"`
}

type apiErrorBody struct {
	Error string `json:"error"`
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return "request failed"
	}
	return e.Message
}

type UploadFailedError struct {
	Status string
}

func (e *UploadFailedError) Error() string {
	return "upload finished with status " + e.Status
}

type UploadInput struct {
	StorageID      string
	Token          string
	LocalPath      string
	RemotePath     string
	RemoteFilename string
	UploadID       string
	FileSize       int64 // C5: used for adaptive progress polling interval
	OnConflict     string // "keep_both" (default) or "skip"
	OnProgress     func(UploadProgress)
}
