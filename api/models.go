package api

type createUserRequest struct {
	// Username is the username of the user
	Username string `json:"username" binding:"required" example:"alice"`
}

type createUserCredentialRequest struct {
	// PublicKey is the public key of the user
	PublicKey string `json:"public_key" binding:"required" example:"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDZ cardno:000607000043"`
}

type createUserCredentialResponse struct {
	// ID is the ID of the user credential created
	ID        uint   `json:"id" example:"10"`

	// Username is the username of the user
	Username  string `json:"username" example:"alice"`

	// PublicKey is the public key of the user
	PublicKey string `json:"public_key" example:"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDZ cardno:000607000043"`

	// CreatedAt is the time when the user credential is added and it has the format of RFC3339
	CreatedAt string `json:"created_at" example:"2024-01-01T00:00:00Z"`
}

type createdUserResponse struct {
	// Username is the username of the user
	Username string `json:"username" example:"alice"`
}

type credentialInfo struct {
	// ID is the ID of the user credential
	Id        uint   `json:"id" example:"10"`

	// PublicKey is the public key of the user
	PublicKey string `json:"public_key" example:"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDZ cardno:000607000043"`
}
