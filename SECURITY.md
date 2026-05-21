# Security Policy

ImagePadServer is intended to run on a user's local machine and may expose a TCP port through UPnP.

## Reporting

Please report security issues privately if this project is hosted on a platform that supports private vulnerability reporting. If private reporting is unavailable, avoid posting exploit details publicly before maintainers have time to respond.

## Scope

Security-sensitive areas include:

- Upload handling
- Image decoding and conversion
- UPnP port mapping
- Public image serving
- Browser UI endpoints

## Operational Guidance

- Run ImagePadServer only on trusted networks.
- Do not share the browser management URL with untrusted users.
- Stop the application when it is no longer needed.
- Review your router's UPnP settings if you do not want automatic port mappings.
