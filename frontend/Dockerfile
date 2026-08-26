# The interface, built once and served by Node.
#
# Two stages: a builder that has the toolchain, and a runtime that has the
# server and nothing else. Next's standalone output traces the modules the
# application actually reaches, so the image carries neither node_modules nor a
# package manager.

FROM node:22-alpine AS build
WORKDIR /src

# Dependencies first: this layer is rebuilt only when the lockfile changes.
COPY package.json package-lock.json ./
RUN npm ci

COPY . .

# The build inlines nothing that the runtime decides. API_ORIGIN, the locale and
# the theme are all read per request on the server, so one image runs in every
# environment.
RUN npm run build

FROM node:22-alpine AS runtime
WORKDIR /app

ENV NODE_ENV=production
ENV PORT=3000
ENV HOSTNAME=0.0.0.0

# `next build` splits standalone output three ways, and all three are needed:
# the traced server, the static chunks it references, and anything in public.
COPY --from=build /src/.next/standalone ./
COPY --from=build /src/.next/static ./.next/static
COPY --from=build /src/public ./public

USER node
EXPOSE 3000

CMD ["node", "server.js"]
