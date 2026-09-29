# Packages a manager binary that was built on the host (see build-and-deploy.sh).
# Same runtime layout as the repository's root Dockerfile, which compiles inside
# Docker instead (build-and-deploy.sh uses that one when FULL_BUILD=true).
FROM alpine:3.18
COPY manager /usr/local/bin/manager
COPY entrypoint.sh /usr/local/bin/
ENTRYPOINT ["entrypoint.sh"]
CMD ["manager"]
