
01 00 00 00 00 03 0d 00 00 01 0f 00 01 00 00 00  ................
03 00 00 00 03 07 5b 04                          ......[.
struct load_command __macho_load_command_[10] = 
{
    enum load_command_type_t cmd = LC_SOURCE_VERSION
    uint32_t cmdsize = 0x10
}
00 00 00 00 00 a9 e0 00                          ........
struct dylib_command __macho_load_command_[11] = 
{
    enum load_command_type_t cmd = LC_LOAD_DYLIB
    uint32_t cmdsize = 0x60
    struct dylib dylib = 
    {
        uint32_t name = 0x18
        uint32_t timestamp = 0x2
        uint32_t current_version = 0xd20202
        uint32_t compatibility_version = 0x10000
    }
}
char data_400770[0x48] = "/System/Library/Frameworks/Hypervisor.framework/Versions/A/Hypervisor\x00\x00", 0
struct dylib_command __macho_load_command_[12] = 
{
    enum load_command_type_t cmd = LC_LOAD_DYLIB
    uint32_t cmdsize = 0x38
    struct dylib dylib = 
    {
        uint32_t name = 0x18
        uint32_t timestamp = 0x2
        uint32_t current_version = 0x5470000
        uint32_t compatibility_version = 0x10000
    }
}
char data_4007d0[0x20] = "/usr/lib/libSystem.B.dylib\x00\x00\x00\x00\x00", 0
struct dylib_command __macho_load_command_[13] = 
{
    enum load_command_type_t cmd = LC_LOAD_DYLIB
    uint32_t cmdsize = 0x30
