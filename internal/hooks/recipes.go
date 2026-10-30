package hooks

// DoctorCondition is the default condition of the doctor hook: it examines
// the machine only while it is under load.
const DoctorCondition = "load>2/core for 5m or memory>85% or pressure.memory>20%"
