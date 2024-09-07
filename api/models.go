package api

type createUserRequest struct {
	Username string `json:"username" binding:"required"`
}

type createUserCredentialRequest struct {
	PublicKey string `json:"public_key" binding:"required"`
}

type createUserCredentialResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	PublicKey string `json:"public_key"`
	CreatedAt string `json:"created_at"`
}

type createdUserResponse struct {
	Username string `json:"username"`
}

type credentialInfo struct {
	Id        uint   `json:"id"`
	PublicKey string `json:"public_key"`
}
