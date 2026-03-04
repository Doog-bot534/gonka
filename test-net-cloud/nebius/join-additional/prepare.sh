#!/bin/bash
if [ -n "$SSH_KEY_PATH" ]; then
  SSH_KEY_ARG="-i $SSH_KEY_PATH"
else
  SSH_KEY_ARG=""
fi

scp $SSH_KEY_ARG -P 18227 launch.py decentai@xj7-5.s.filfox.io:/srv/dai
scp $SSH_KEY_ARG -P 18227 join-additional/18227.sh decentai@xj7-5.s.filfox.io:/srv/dai/join.sh
scp $SSH_KEY_ARG -P 18228 launch.py decentai@xj7-5.s.filfox.io:/srv/dai
scp $SSH_KEY_ARG -P 18228 join-additional/18228.sh decentai@xj7-5.s.filfox.io:/srv/dai/join.sh
