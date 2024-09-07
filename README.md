# file-server

Secure file server with the following characteristics.

- Public key cryptography is used for authentication
- User information is stored in a PostgreSQL database
- No shell file access

:warning: This is a work in progress and not ready for production yet :warning:

database tables
- users
- user_keys

environment variables
- file path to database connection string
- file path to host private key
- server port
- directory path to data storage
- list of administrative users
