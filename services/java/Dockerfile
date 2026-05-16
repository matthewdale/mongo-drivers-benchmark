# syntax=docker/dockerfile:1.6
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src

# Cache dependencies layer.
COPY pom.xml ./
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q -DskipTests dependency:go-offline

# Build.
COPY src ./src
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q -DskipTests package

FROM eclipse-temurin:21-jre
COPY --from=build /src/target/*-shaded.jar /app.jar
EXPOSE 8080
ENTRYPOINT ["java","-jar","/app.jar"]
