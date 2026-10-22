import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val version = Properties().apply { file("../version.properties").inputStream().use { load(it) } }

android {
    namespace = "dev.dyna.call"
    compileSdk = 35

    defaultConfig {
        applicationId = "dev.dyna.call"
        minSdk = 29
        targetSdk = 34
        versionCode = version.getProperty("versionCode").toInt()
        versionName = "0.$versionCode"
    }
    // build-apk.sh signs with the one key kept in Dyna's data dir, so updates install over
    // the old app. Without it (a plain ./gradlew build) the debug key signs.
    val keystore = System.getenv("DYNA_KEYSTORE")
    if (keystore != null) {
        signingConfigs.create("dyna") {
            storeFile = file(keystore)
            storePassword = System.getenv("DYNA_KEYSTORE_PASS")
            keyAlias = "dyna"
            keyPassword = System.getenv("DYNA_KEYSTORE_PASS")
        }
        buildTypes.getByName("debug").signingConfig = signingConfigs.getByName("dyna")
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures { buildConfig = true }
    kotlinOptions { jvmTarget = "17" }
    testOptions { unitTests.isReturnDefaultValues = true }
}

dependencies {
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("androidx.core:core:1.13.1")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}
